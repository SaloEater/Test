package connections

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gocloud.dev/pubsub/driver"
	"net/url"
	"strconv"
	"time"
)

func NewJetstream(js jetstream.JetStream) Connection {
	return &jetstreamConnection{jetStream: js}
}

type jetstreamConnection struct {
	// Connection to use for communication with the server.
	jetStream jetstream.JetStream
}

func (c *jetstreamConnection) Raw() interface{} {
	return c.jetStream
}

func (c *jetstreamConnection) CreateTopic(ctx context.Context, opts *TopicOptions) (Topic, error) {

	return &jetstreamTopic{subject: opts.Subject, jetStream: c.jetStream}, nil
}

func (c *jetstreamConnection) CreateSubscription(ctx context.Context, opts *SubscriptionOptions) (Queue, error) {

	stream, err := c.jetStream.Stream(ctx, opts.StreamName)
	if err != nil &&
		errors.Is(err, nats.ErrStreamNotFound) {
		return nil, err
	}

	if stream == nil {

		streamConfig := jetstream.StreamConfig{
			Name:         opts.StreamName,
			Description:  opts.StreamDescription,
			Subjects:     opts.Subjects,
			MaxConsumers: opts.ConsumersMaxCount,
		}

		stream, err = c.jetStream.CreateStream(ctx, streamConfig)
		if err != nil {
			return nil, err
		}

	}

	// Create durable consumer
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:      opts.ConsumerName,
		Durable:   opts.DurableQueue,
		AckPolicy: jetstream.AckExplicitPolicy,

		AckWait:            time.Duration(opts.ConsumerAckWaitTimeoutMs) * time.Millisecond,
		MaxWaiting:         opts.ConsumerMaxWaiting,
		MaxAckPending:      opts.ConsumerMaxAckPending,
		MaxRequestExpires:  time.Duration(opts.ConsumerRequestTimeoutMs) * time.Millisecond,
		MaxRequestBatch:    opts.ConsumerRequestBatch,
		MaxRequestMaxBytes: opts.ConsumerRequestMaxBatchBytes,
	})
	if err != nil {
		return nil, err
	}

	return &jetstreamConsumer{consumer: consumer}, nil

}

type jetstreamTopic struct {
	subject   string
	jetStream jetstream.JetStream
}

func (t *jetstreamTopic) Subject() string {
	return t.subject
}

func (t *jetstreamTopic) PublishMessage(ctx context.Context, msg *nats.Msg) (string, error) {
	var ack *jetstream.PubAck
	var err error
	if ack, err = t.jetStream.PublishMsg(ctx, msg); err != nil {
		return "", err
	}

	return strconv.Itoa(int(ack.Sequence)), nil
}

type jetstreamConsumer struct {
	consumer jetstream.Consumer
}

func (jc *jetstreamConsumer) IsDurable() bool {
	return true
}

func (jc *jetstreamConsumer) Unsubscribe() error {
	return nil
}

func (jc *jetstreamConsumer) ReceiveMessages(_ context.Context, batchCount int) ([]*driver.Message, error) {

	var messages []*driver.Message

	if batchCount <= 0 {
		batchCount = 1
	}

	msgBatch, err := jc.consumer.Fetch(batchCount)
	if err != nil {
		return nil, err
	}

	for msg := range msgBatch.Messages() {

		driverMsg, err0 := decodeJetstreamMessage(msg)

		if err0 != nil {
			return nil, err0
		}

		messages = append(messages, driverMsg)
	}

	return messages, nil
}

func (jc *jetstreamConsumer) Ack(ctx context.Context, ids []driver.AckID) error {
	for _, id := range ids {
		msg, ok := id.(jetstream.Msg)
		if !ok {
			continue
		}

		// We don;t use DoubleAck as it fails conformance tests
		_ = msg.DoubleAck(ctx)
	}

	return nil
}

func (jc *jetstreamConsumer) Nack(ctx context.Context, ids []driver.AckID) error {

	for _, id := range ids {
		msg, ok := id.(jetstream.Msg)
		if !ok {
			continue
		}

		_ = msg.Nak()

	}

	return nil
}

func jsMessageAsFunc(msg jetstream.Msg) func(interface{}) bool {
	return func(i interface{}) bool {
		if p, ok := i.(*jetstream.Msg); ok {
			*p = msg
			return true
		}

		return false
	}
}

func decodeJetstreamMessage(msg jetstream.Msg) (*driver.Message, error) {
	if msg == nil {
		return nil, nats.ErrInvalidMsg
	}

	dm := driver.Message{
		AsFunc: jsMessageAsFunc(msg),
		Body:   msg.Data(),
	}

	if msg.Headers() != nil {
		dm.Metadata = map[string]string{}
		for k, v := range msg.Headers() {
			var sv string
			if len(v) > 0 {
				sv = v[0]
			}
			kb, err := url.QueryUnescape(k)
			if err != nil {
				return nil, err
			}
			vb, err := url.QueryUnescape(sv)
			if err != nil {
				return nil, err
			}
			dm.Metadata[kb] = vb
		}
	}

	dm.AckID = msg

	return &dm, nil
}
