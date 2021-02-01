package main

import (
	"log"
	"time"

	"github.com/zephyr-im/krb5-go"
	"github.com/zephyr-im/zephyr-go"
)

func main() {
	session, err := zephyr.DialSystemDefault()
	if err != nil {
		log.Fatal(err)
	}
	// Make sure the notice sink doesn't get stuck.
	// TODO(davidben): This is silly.
	go func() {
		for _ = range session.Messages() {
		}
	}()

	ctx, err := krb5.NewContext()
	if err != nil {
		log.Fatal(err)
	}
	defer ctx.Free()
	ack, err := session.SendMessage(ctx, &zephyr.Message{
		Header: zephyr.Header{
			Kind:  zephyr.ACKED,
			UID:   session.MakeUID(time.Now()),
			Port:  session.Port(),
			Class: "message", Instance: "personal", OpCode: "test",
			Sender:        session.Sender(),
			Recipient:     "mrittenb@ATHENA.MIT.EDU",
			DefaultFormat: "http://mit.edu/df/",
			SenderAddress: session.LocalAddr().IP,
			Charset:       zephyr.CharsetUTF8,
			OtherFields:   nil,
		},
		Body: []string{"mattermost.xvm.mit.edu", "Hello world"},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ack: %v", ack)
}
