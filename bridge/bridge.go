package bridge

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/sipb/mm2zephyr/mm"
	"github.com/sipb/mm2zephyr/prettier"
	"github.com/sipb/mm2zephyr/zephyr"
	z "github.com/zephyr-im/zephyr-go"
	"golang.org/x/sync/errgroup"
)

// Config represents the configuration for the Mattermost-Zephyr bridge.
type Config struct {
	Mattermost MattermostConfig `yaml:"mattermost"`
	// Mappings represents the list of Mattermost channel to Zephyr triplet pairings.
	// If multiple mappings match a Zephyrgram, the first one will be used.
	Mappings []Mapping `yaml:"mappings"`
}

// MattermostConfig represents the configuration for connecting to Mattermost.
type MattermostConfig struct {
	URL string `yaml:"url"`
}

// Mapping objects represent a single pairing of Mattermost channel and Zephyr triplet.
type Mapping struct {
	Channel  string `yaml:"channel"`
	Class    string `yaml:"class"`
	Instance string `yaml:"instance"`
}

// Bridge encapsulates all the long-term state of the bridge.
type Bridge struct {
	config   Config
	token    string
	mu       sync.Mutex
	lastpost map[lpkey]*model.Post
	pmu      sync.Mutex
	prettier *prettier.Prettier
}

type lpkey struct {
	class, instance string
}

// New constructs a new Bridge object.
func New(config Config, token string) (*Bridge, error) {
	p, err := prettier.New()
	if err != nil {
		return nil, err
	}
	return &Bridge{
		config:   config,
		token:    token,
		lastpost: make(map[lpkey]*model.Post),
		prettier: p,
	}, nil
}

// Run the bridge until ctx is canceled.
func (b *Bridge) Run(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		bot, err := mm.New(b.config.Mattermost.URL, b.token)
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

		// TODO: Remove this when zephyr-go learns how to reload its tickets.
		expirationTime := client.TicketExpirationTime()
		eg.Go(func() error {
			select {
			case <-time.After(time.Until(expirationTime) - time.Minute):
				return fmt.Errorf("ticket about to expire")
			case <-ctx.Done():
			}
			return ctx.Err()
		})

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
			if err := b.updateHeader(bot, mmChannel, mapping); err != nil {
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
					rootID := b.getRootID(message.Class, message.Instance)

					if rootID == "" && instance == "*" && strings.ToLower(message.Instance) != "personal" {
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
					if post.Post.IsJoinLeaveMessage() {
						// Drop join/leave messages
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
					if fmt, err := b.formatMarkdown(message); err != nil {
						log.Printf("failed to format a message: %v", err)
					} else {
						message = fmt
					}
					sender := strings.TrimPrefix(post.Sender, "@")
					zsig := bot.GetPostLink(post.Post)
					if err := client.SendMessage(sender, mapping.Class, instance, []string{zsig, message}); err != nil {
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

func (b *Bridge) formatMarkdown(in string) (string, error) {
	b.pmu.Lock()
	defer b.pmu.Unlock()
	return b.prettier.Format(in, map[string]interface{}{
		"proseWrap": "always",
		"parser":    "markdown",
	})
}

func (b *Bridge) updateHeader(bot *mm.Bot, mmChannel *model.Channel, mapping Mapping) error {
	// TODO: Update the header if it already has the wrong class?
	// (Note that care needs to be taken if there are multiple mappings for a single channel.)
	if !strings.HasPrefix(mmChannel.Header, "[-") {
		header := fmt.Sprintf("[-c %s]", mapping.Class)
		if mapping.Instance != "" {
			header = fmt.Sprintf("[-c %s -i %s]", mapping.Class, mapping.Instance)
		}
		if mmChannel.Header != "" {
			header += " " + mmChannel.Header
		}
		return bot.UpdateChannelHeader(mmChannel, header)
	}
	return nil
}

// recordPost updates the most recent post for a given class, instance.
func (b *Bridge) recordPost(class, instance string, post *model.Post) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastpost[lpkey{strings.ToLower(class), strings.ToLower(instance)}] = post
}

// getRootID returns the root ID that should be used for a message.
// If there is no previous message, it returns an empty string.
func (b *Bridge) getRootID(class, instance string) string {
	class = strings.ToLower(class)
	instance = strings.ToLower(instance)
	// TODO: Store this in a stateful way?
	// TODO: Time limit on how old the last post can be?
	b.mu.Lock()
	defer b.mu.Unlock()
	post := b.lastpost[lpkey{class, instance}]
	if post == nil {
		return ""
	}
	if post.RootId != "" {
		return post.RootId
	}
	return post.Id
}

var instanceRE = regexp.MustCompile(`^\[\s*-i\s+([^]]+?)\s*\]\s*`)

// findInstance extracts the instance that a given post should be sent on.
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

// logMessage logs a Zephyr message.
func logMessage(message *z.Message) {
	body := message.Body[0]
	zsig := message.Header.Sender
	if len(message.Body) > 1 {
		body = message.Body[1]
		zsig = message.Body[0]
	}
	log.Printf("[-c %s -i %s] %s <%s>: %s", message.Class, message.Instance, zsig, message.Header.Sender, body)
}

// logPost logs a Mattermost post.
func logPost(mapping Mapping, post mm.PostNotification) {
	log.Printf("%s: <%s> %#v", mapping.Channel, post.Sender, post.Post)
}
