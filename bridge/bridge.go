package bridge

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/sipb/mm2zephyr/mm"
	"github.com/sipb/mm2zephyr/zephyr"
	z "github.com/zephyr-im/zephyr-go"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Mappings []Mapping `yaml:"mappings"`
}

type Mapping struct {
	Channel  string `yaml:"channel"`
	Class    string `yaml:"class"`
	Instance string `yaml:"instance"`
}

type Bridge struct {
	config   Config
	token    string
	mu       sync.Mutex
	lastpost map[lpkey]*model.Post
}

type lpkey struct {
	class, instance string
}

func New(config Config, token string) *Bridge {
	return &Bridge{
		config:   config,
		token:    token,
		lastpost: make(map[lpkey]*model.Post),
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		bot, err := mm.New(b.token)
		if err != nil {
			return err
		}

		eg.Go(func() error {
			<-ctx.Done()
			bot.Close()
			return ctx.Err()
		})

		personalsCh := bot.ListenPersonals()
		eg.Go(func() error {
			for post := range personalsCh {
				if post.Post.Message == "/restart" {
					return fmt.Errorf("restart requested by %s", post.Sender)
				}
			}
			return nil
		})

		client, err := zephyr.NewClient()
		if err != nil {
			return err
		}

		eg.Go(func() error {
			<-ctx.Done()
			client.Close()
			return ctx.Err()
		})

		for _, mapping := range b.config.Mappings {
			// Make a local copy for the closure
			mapping := mapping
			instance := mapping.Instance
			if instance == "" {
				instance = "*"
			}
			zgramCh, err := client.SubscribeAndListen(mapping.Class, instance)
			if err != nil {
				return err
			}
			// mmChannel is a Mattermost channel, NOT a Go channel.
			mmChannel, postCh, err := bot.ListenChannel(mapping.Channel)
			if err != nil {
				return err
			}
			eg.Go(func() error {
				for message := range zgramCh {
					if message.Header.OpCode == "mattermost" {
						continue
					}
					logMessage(message)
					username := message.Header.Sender
					username = strings.TrimSuffix(username, "@ATHENA.MIT.EDU")
					messageText := message.Body[1]
					rootID, normalizedInstance := b.getRootID(message.Class, message.Instance)

					if rootID == "" && instance == "*" && normalizedInstance != "personal" {
						messageText = fmt.Sprintf("[-i %s] %s", message.Instance, messageText)
					}

					post, err := bot.SendPost(&model.Post{
						ChannelId: mmChannel.Id,
						Message:   messageText,
						Props: model.StringInterface{
							"override_username": username,
							"from_zephyr":       "true",
							"class":             message.Class,
							"instance":          message.Instance,
						},
						ParentId: rootID,
						RootId:   rootID,
					})
					if err != nil {
						return err
					}
					b.recordPost(message.Class, message.Instance, post)
				}
				return nil
			})
			eg.Go(func() error {
				for post := range postCh {
					logPost(mapping, post)
					if _, ok := post.Post.Props["from_bot"]; ok {
						// Drop any message from a bot (including ourselves)
						continue
					}
					message := post.Post.Message
					instance := mapping.Instance
					if instance == "" {
						var err error
						instance, err = b.findInstance(bot, post.Post)
						if err != nil {
							log.Printf("error determining instance: %v", err)
						}
						message = instanceRE.ReplaceAllString(message, "")
					}
					if instance == "" {
						instance = "personal"
					}
					b.recordPost(mapping.Class, instance, post.Post)
					sender := strings.TrimPrefix(post.Sender, "@")
					// TODO: Set zsig to a pointer to the channel or post
					if err := client.SendMessage(sender, mapping.Class, instance, message); err != nil {
						log.Printf("sending message: %v", err)
						return err
					}
				}
				return nil
			})
		}
		return nil
	})
	return eg.Wait()
}

func (b *Bridge) recordPost(class, instance string, post *model.Post) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastpost[lpkey{class, instance}] = post
}

func (b *Bridge) getRootID(class, instance string) (string, string) {
	instance = strings.ToLower(instance)
	// TODO: Store this in a stateful way?
	// TODO: Time limit on how old the last post can be?
	b.mu.Lock()
	defer b.mu.Unlock()
	post := b.lastpost[lpkey{class, instance}]
	if post == nil {
		return "", instance
	}
	if post.RootId != "" {
		return post.RootId, instance
	}
	return post.Id, instance
}

var instanceRE = regexp.MustCompile(`^\[\s*-i\s+([^]]+?)\s*\]\s*`)

func (b *Bridge) findInstance(bot *mm.Bot, post *model.Post) (string, error) {
	if matches := instanceRE.FindStringSubmatch(post.Message); matches != nil {
		return matches[1], nil
	}
	list, err := bot.GetPostThread(post.Id)
	if err != nil {
		return "", err
	}
	for i := len(list.Order) - 1; i >= 0; i-- {
		post := list.Posts[list.Order[i]]
		if instance := post.GetProp("instance"); instance != nil {
			if instance, ok := instance.(string); ok {
				return instance, nil
			}
		}
		if matches := instanceRE.FindStringSubmatch(post.Message); matches != nil {
			return matches[1], nil
		}
	}
	return "", nil
}

func logMessage(message *z.Message) {
	body := message.Body[0]
	zsig := message.Header.Sender
	if len(message.Body) > 1 {
		body = message.Body[1]
		zsig = message.Body[0]
	}
	log.Printf("[-c %s -i %s] %s <%s>: %s", message.Class, message.Instance, zsig, message.Header.Sender, body)
}

func logPost(mapping Mapping, post mm.PostNotification) {
	log.Printf("%s: <%s> %#v", mapping.Channel, post.Sender, post.Post)
}
