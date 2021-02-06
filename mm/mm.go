package mm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"github.com/mattermost/mattermost-server/v5/model"
	"golang.org/x/sync/errgroup"
)

type Bot struct {
	//webSocketClient *model.WebSocketClient
	client   *model.Client4
	team     *model.Team
	channels map[string]*model.Channel
	close    func()
}

const url = "https://mattermost.mit.edu"

func New(token string) (*Bot, error) {
	client := model.NewAPIv4Client(url)
	// Check if server is running
	if props, resp := client.GetOldClientConfig(""); resp.Error != nil {
		return nil, resp.Error
	} else {
		log.Print("Server detected and is running version " + props["Version"])
	}
	client.SetOAuthToken(token)
	teams, resp := client.GetAllTeams("", 0, 2)
	if resp.Error != nil {
		return nil, resp.Error
	}
	if len(teams) != 1 {
		return nil, fmt.Errorf("got %d teams, expected 1", len(teams))
	}

	b := &Bot{
		client:   client,
		team:     teams[0],
		channels: make(map[string]*model.Channel),
	}

	etag := ""
	for page := 0; true; page++ {
		channels, resp := client.GetPublicChannelsForTeam(teams[0].Id, page, 60, etag)
		if resp.Error != nil {
			return nil, resp.Error
		}
		etag = resp.Etag
		for _, ch := range channels {
			log.Printf("found channel: %#v", ch)
			b.channels[ch.Name] = ch
		}
		if len(channels) < 60 {
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go b.listenLoop(ctx, token)
	b.close = cancel

	return b, nil
}

// listenLoop repeatedly connects to the WebSocket API, reconnecting whenever the connection is closed.
func (bot *Bot) listenLoop(ctx context.Context, token string) {
	for {
		if err := bot.listen(ctx, token); err != nil {
			log.Printf("websocket connection failed: %v", err)
			time.Sleep(time.Second)
		}
		select {
		case <-ctx.Done():
			return
		}
	}
}

func (bot *Bot) listen(ctx context.Context, token string) error {
	wsClient, wserr := model.NewWebSocketClient(strings.Replace(url, "https://", "wss://", 1), token)
	if wserr != nil {
		return wserr
	}
	defer wsClient.Close()
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		<-ctx.Done()
		wsClient.Close()
		return ctx.Err()
	})
	eg.Go(func() error {
		for _ = range wsClient.PingTimeoutChannel {
			log.Print("mattermost ping timeout")
			// Returning an error will close the connection and trigger a reconnect.
			return errors.New("mattermost ping timeout")
		}
		return nil
	})
	eg.Go(func() error {
		for ev := range wsClient.EventChannel {
			switch ev.Event {
			case model.WEBSOCKET_EVENT_POSTED:
				post := model.PostFromJson(strings.NewReader(ev.Data["post"].(string)))
				log.Printf("received mattermost post: %#v", post)
			default:
				log.Printf("received mattermost event: %#v", ev)
			}
		}
		return nil
	})
	eg.Go(func() error {
		for response := range wsClient.ResponseChannel {
			log.Printf("received mattermost response: %#v", response)
		}
		return nil
	})
	// Listen spawns a goroutine
	wsClient.Listen()
	return eg.Wait()
}

func (bot *Bot) Close() {
	bot.close()
}

func (bot *Bot) ListenChannel(channel_name string) (<-chan *model.Post, error) {
	// Make sure the bot has joined the channel
	// Subscribe to posts
	return nil, errors.New("unimplemented")
}

func (bot *Bot) SendMessageToChannel(channel_name string, message, username string) error {
	ch, ok := bot.channels[channel_name]
	if !ok {
		return fmt.Errorf("unknown channel %q", channel_name)
	}
	post := &model.Post{
		ChannelId: ch.Id,
		Message:   message,
	}
	post.AddProp("override_username", username)
	post.AddProp("from_webhook", "true")
	_, resp := bot.client.CreatePost(post)
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

func (bot *Bot) SendSpoofedMessageToChannel(name string) error {
	var webhook *model.IncomingWebhook
	webhooks, resp := bot.client.GetIncomingWebhooks(0, 100, "")
	if resp.Error != nil {
		return resp.Error
	}
	for _, wh := range webhooks {
		if wh.DisplayName == "zephyr" {
			webhook = wh
		}
	}
	if webhook == nil {
		webhook, resp = bot.client.CreateIncomingWebhook(&model.IncomingWebhook{
			ChannelId:   bot.channels[name].Id,
			DisplayName: "zephyr",
			Description: "Zephyr bridge",
		})
		if resp.Error != nil {
			return resp.Error
		}
	}
	log.Printf("webhook: %+v", webhook)
	req := &model.IncomingWebhookRequest{
		ChannelName: "Test",
		Text:        "Hello from webhook",
		Username:    "quentin",
	}
	body := req.ToJson()
	log.Printf("will post: %v", body)
	url := fmt.Sprintf("%s/hooks/%s", bot.client.Url, webhook.Id)
	r, err := bot.client.HttpClient.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("failed to post: %v", r.Status)
	}
	rbody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return err
	}
	log.Printf("server said: %v", string(rbody))
	return nil
}
