package bridge

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

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
	lastpost map[instance]*model.Post
}

type instance struct {
	class, instance string
}

func New(config Config, token string) *Bridge {
	return &Bridge{
		config:   config,
		token:    token,
		lastpost: make(map[instance]*model.Post),
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	bot, err := mm.New(b.token)
	if err != nil {
		return err
	}

	client, err := zephyr.NewClient()
	if err != nil {
		return err
	}

	eg, ctx := errgroup.WithContext(ctx)
	for _, mapping := range b.config.Mappings {
		// Make a local copy for the closure
		mapping := mapping
		instance := mapping.Instance
		if instance == "" {
			instance = "*"
		}
		ch, err := client.SubscribeAndListen(mapping.Class, instance)
		if err != nil {
			return err
		}
		channel, mmch, err := bot.ListenChannel(mapping.Channel)
		if err != nil {
			return err
		}
		eg.Go(func() error {
			for message := range ch {
				if message.Header.OpCode == "mattermost" {
					continue
				}
				logMessage(message)
				username := message.Header.Sender
				messageText := message.Body[1]

				if instance == "*" && message.Instance != "personal" {
					messageText = fmt.Sprintf("[-i %s] %s", message.Instance, messageText)
				}

				_, err = bot.SendMessageToChannel(channel, messageText, model.StringInterface{
					"override_username": username,
					"instance":          message.Instance,
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
		eg.Go(func() error {
			for post := range mmch {
				logPost(mapping, post)
				if _, ok := post.Post.Props["from_bot"]; ok {
					// Drop any message from a bot (including ourselves)
					continue
				}
				message := post.Post.Message
				instance := mapping.Instance
				// TODO: parse instance from message or its parents
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
				if err := client.SendMessage(strings.TrimPrefix(post.Sender, "@"), mapping.Class, instance, message); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return eg.Wait()
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
