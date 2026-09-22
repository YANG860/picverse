package mq

import (
	"fmt"
	"rednote/packages/utils"
	"time"

	"google.golang.org/protobuf/proto"
)

type MQMessage struct {
	ID int64 `json:"id"`

	Subject string `json:"subject"`
	SendAt  int64  `json:"send_at"`

	Header  map[string]string `json:"header"`
	PayLoad []byte            `json:"payload"`
}

func NewMQMessage(subject string, header map[string]string, payload proto.Message) (*MQMessage, error) {
	payloadBytes, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message payload: %w", err)
	}

	return &MQMessage{
		ID:      utils.GenID().Int64(),
		SendAt:  time.Now().UnixMilli(),
		Subject: subject,
		Header:  header,
		PayLoad: payloadBytes,
	}, nil
}

func (m *MQMessage) WithDelay(t time.Duration) *MQMessage {
	m.SendAt = time.Now().Add(t).UnixMilli()
	return m
}

func (m *MQMessage) WithID(id int64) *MQMessage {
	m.ID = id
	return m
}
