package zephyr

import (
	"log"
	"sync"
	"time"

	"github.com/zephyr-im/krb5-go"
	"github.com/zephyr-im/zephyr-go"
)

type listener struct {
	class, instance string
	ch              chan<- *zephyr.Message
}

type Client struct {
	session *zephyr.Session
	kCtx    *krb5.Context

	mu        sync.Mutex
	listeners []listener
}

func NewClient() (*Client, error) {
	session, err := zephyr.DialSystemDefault()
	if err != nil {
		return nil, err
	}

	ctx, err := krb5.NewContext()
	if err != nil {
		log.Fatal(err)
	}
	c := &Client{
		session: session,
		kCtx:    ctx,
	}
	go c.listen()
	return c, nil
}

func (c *Client) listen() {
	for result := range c.session.Messages() {
		log.Printf("received message: %#v", result)
		// TODO: Do something with result.AuthStatus?
		msg := result.Message
		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg *zephyr.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.listeners {
		if l.class == msg.Class && (l.instance == "*" || l.instance == msg.Instance) {
			l.ch <- msg
			return
		}
	}
	log.Printf("unhandled message: %v", msg)
}

func (c *Client) SubscribeAndListen(class, instance string) (<-chan *zephyr.Message, error) {
	if ack, err := c.session.SendSubscribeNoDefaults(c.kCtx, []zephyr.Subscription{{Class: class, Instance: instance}}); err != nil {
		return nil, err
	} else {
		log.Printf("Subscribed to (%q, %q): %#v", class, instance, ack)
	}
	ch := make(chan *zephyr.Message)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listeners = append(c.listeners, listener{
		class:    class,
		instance: instance,
		ch:       ch,
	})
	return ch, nil
}

func (c *Client) SendMessage(sender, class, instance, message string) error {
	ack, err := c.session.SendMessage(c.kCtx, &zephyr.Message{
		Header: zephyr.Header{
			Kind:  zephyr.ACKED,
			UID:   c.session.MakeUID(time.Now()),
			Port:  c.session.Port(),
			Class: class, Instance: instance, OpCode: "",
			Sender:        sender,
			Recipient:     "",
			DefaultFormat: "http://mit.edu/df/",
			SenderAddress: c.session.LocalAddr().IP,
			Charset:       zephyr.CharsetUTF8,
			OtherFields:   nil,
		},
		Body: []string{"mattermost.mit.edu", message},
	})
	if err != nil {
		return err
	}
	log.Printf("ack: %v", ack)
	return nil
}

func (c *Client) Close() {
	c.kCtx.Free()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.listeners {
		close(l.ch)
	}
}
