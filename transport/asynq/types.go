package asynq

type MessagePayload any

type Creator func() any

type MessageHandler func(string, MessagePayload) error
