package mm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost-server/v5/model"
	"golang.org/x/sync/errgroup"
)

type listener struct {
	channelID string
	ch        chan<- PostNotification
}

type Bot struct {
	//webSocketClient *model.WebSocketClient
	client   *model.Client4
	user     *model.User
	team     *model.Team
	channels map[string]*model.Channel
	close    func()

	mu          sync.Mutex
	listeners   []listener
	personalsCh chan<- PostNotification
}

func New(url, token string) (*Bot, error) {
	client := model.NewAPIv4Client(url)
	// Check if server is running
	if props, resp := client.GetOldClientConfig(""); resp.Error != nil {
		return nil, resp.Error
	} else {
		log.Print("Server detected and is running version " + props["Version"])
	}
	client.SetOAuthToken(token)
	user, resp := client.GetMe("")
	if resp.Error != nil {
		return nil, resp.Error
	}
	teams, resp := client.GetAllTeams("", 0, 2)
	if resp.Error != nil {
		return nil, resp.Error
	}
	if len(teams) != 1 {
		return nil, fmt.Errorf("got %d teams, expected 1", len(teams))
	}

	b := &Bot{
		client:   client,
		user:     user,
		team:     teams[0],
		channels: make(map[string]*model.Channel),
	}

	// TODO: Parse channel headers for class/instance information?
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
	wsClient, wserr := model.NewWebSocketClient(strings.Replace(bot.client.Url, "https://", "wss://", 1), token)
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
		for range wsClient.PingTimeoutChannel {
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
				post := model.PostFromJson(strings.NewReader(ev.GetData()["post"].(string)))
				sender := ev.GetData()["sender_name"].(string)
				bot.handlePost(PostNotification{
					Post:        post,
					ChannelType: ev.GetData()["channel_type"].(string),
					Sender:      sender,
				})
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

func (bot *Bot) handlePost(post PostNotification) {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if post.ChannelType == model.CHANNEL_DIRECT && bot.personalsCh != nil {
		bot.personalsCh <- post
		return
	}
	for _, l := range bot.listeners {
		if l.channelID == post.Post.ChannelId {
			l.ch <- post
			return
		}
	}
	log.Printf("unhandled post from %q: %#v", post.Sender, post.Post)
}

func (bot *Bot) Close() {
	bot.close()
	bot.mu.Lock()
	defer bot.mu.Unlock()
	for _, l := range bot.listeners {
		close(l.ch)
	}
	if bot.personalsCh != nil {
		close(bot.personalsCh)
	}
}

type PostNotification struct {
	Post        *model.Post
	Sender      string
	ChannelType string
}

func (bot *Bot) ListenPersonals() <-chan PostNotification {
	ch := make(chan PostNotification)
	bot.mu.Lock()
	defer bot.mu.Unlock()
	bot.personalsCh = ch
	return ch
}

// Like ListenChannel, but doesn't listen; only prepares for transmission.
func (bot *Bot) AttachChannel(channel_name string) (*model.Channel, error) {
	// Make sure the bot has joined the channel
	ch, resp := bot.client.GetChannelByName(channel_name, bot.team.Id, "")
	if resp.Error != nil {
		return nil, resp.Error
	}
	log.Print(ch)
	_, resp = bot.client.GetChannelMember(ch.Id, bot.user.Id, "")
	if resp.StatusCode == 404 {
		if _, resp := bot.client.AddChannelMember(ch.Id, bot.user.Id); resp.Error != nil {
			return ch, resp.Error
		}
	} else if resp.Error != nil {
		return ch, resp.Error
	}
	return ch, nil
}

func (bot *Bot) ListenChannel(channel_name string) (*model.Channel, <-chan PostNotification, error) {
	// Make sure the bot has joined the channel
	ch, err := bot.AttachChannel(channel_name)
	if err != nil {
		return ch, nil, err
	}
	// Subscribe to posts
	postCh := make(chan PostNotification)
	bot.mu.Lock()
	defer bot.mu.Unlock()
	bot.listeners = append(bot.listeners, listener{
		channelID: ch.Id,
		ch:        postCh,
	})
	return ch, postCh, nil
}

func (bot *Bot) UpdateChannelHeader(channel *model.Channel, header string) error {
	_, resp := bot.client.PatchChannel(channel.Id, &model.ChannelPatch{Header: model.NewString(header)})
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

func (bot *Bot) GetPostThread(postId string) (*model.PostList, error) {
	pl, resp := bot.client.GetPostThread(postId, "")
	if resp.Error != nil {
		return pl, resp.Error
	}
	return pl, nil
}

func (bot *Bot) GetPostLink(post *model.Post) string {
	return fmt.Sprintf("%s/%s/pl/%s", bot.client.Url, bot.team.Name, post.Id)
}

func (bot *Bot) SendPost(post *model.Post) (*model.Post, error) {
	post.AddProp("from_webhook", "true")
	post, resp := bot.client.CreatePost(post)
	if resp.Error != nil {
		return nil, resp.Error
	}
	return post, nil
}

func (bot *Bot) SendMessageToChannel(channel *model.Channel, message string, props model.StringInterface) (*model.Post, error) {
	post := &model.Post{
		ChannelId: channel.Id,
		Message:   message,
		Props:     props,
	}
	post.AddProp("from_webhook", "true")
	post, resp := bot.client.CreatePost(post)
	if resp.Error != nil {
		return nil, resp.Error
	}
	return post, nil
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
