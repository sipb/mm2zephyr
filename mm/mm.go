package mm

import (
	"fmt"
	"io/ioutil"
	"log"
	"strings"

	"github.com/mattermost/mattermost-server/v5/model"
)

type Bot struct {
	client          *model.Client4
	webSocketClient *model.WebSocketClient
	team            *model.Team
	channels        map[string]*model.Channel
}

func New(token string) (*Bot, error) {
	client := model.NewAPIv4Client("https://mattermost.xvm.mit.edu")
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
			b.channels[ch.Name] = ch
		}
		if len(channels) < 60 {
			break
		}
	}

	return b, nil
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
