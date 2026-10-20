// Package mq 封装消息队列的读写。
//
// 当前版本以 Kafka 承载转码任务：网关作为生产者投递任务，
// 转码 Worker 作为消费者拉取任务。业务层只依赖本包暴露的
// Producer / Consumer，不直接接触 Kafka 客户端，便于后续替换中间件。
package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// EnsureTopic 确保 topic 已存在，不存在则按单副本创建。
//
// 背景：消费组在 topic 尚不存在时加入会拿不到分区，之后 topic 才被生产者
// 自动创建时也不会自动恢复，导致「先启动 Worker、后首次上传」时任务丢失。
// 因此在 Worker 启动消费前先把 topic 建好。
func EnsureTopic(brokers []string, topic string, partitions int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &kafka.Client{Addr: kafka.TCP(brokers...), Timeout: 10 * time.Second}
	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:             topic,
			NumPartitions:     partitions,
			ReplicationFactor: 1,
		}},
	})
	if err != nil {
		return fmt.Errorf("创建 topic 失败: %w", err)
	}
	// topic 已存在不算错误，其余错误需要上抛
	for name, e := range resp.Errors {
		if e != nil && !errors.Is(e, kafka.TopicAlreadyExists) {
			return fmt.Errorf("创建 topic %s 失败: %w", name, e)
		}
	}
	return nil
}

// Producer 是消息生产者。
//
// kafka.Writer 采用惰性连接：构造时不会立刻连broker，
// 因此即使 Kafka 暂时不可用，网关也能正常启动（投递时才会报错）。
type Producer struct {
	writer *kafka.Writer
}

// NewProducer 构造生产者。
func NewProducer(brokers []string, topic string) *Producer {
	return &Producer{
		writer: &kafka.Writer{
			Addr:  kafka.TCP(brokers...),
			Topic: topic,
			// LeastBytes 在多个分区间按消息大小做均衡，避免热点分区
			Balancer:     &kafka.LeastBytes{},
			RequiredAcks: kafka.RequireOne,
			BatchTimeout: 10 * time.Millisecond,
			WriteTimeout: 10 * time.Second,
		},
	}
}

// Publish 把 payload 序列化为 JSON 后写入队列。
//
// key 用于让同一条业务实体的消息落到同一分区，保证单分区内有序；
// 转码场景下传视频 ID 即可。
func (p *Producer) Publish(ctx context.Context, key string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(key),
		Value: body,
	})
}

// Close 关闭生产者，刷新未发送的缓冲。
func (p *Producer) Close() error {
	return p.writer.Close()
}

// Handler 处理一条消息。返回 nil 表示处理成功，位移才会被提交。
type Handler func(ctx context.Context, value []byte) error

// Consumer 是消费组消费者。
type Consumer struct {
	reader  *kafka.Reader
	handler Handler
}

// NewConsumer 构造消费者。
//
// group 为消费组：同一组内的多个副本会分摊分区，不会重复消费同一条消息。
// CommitInterval 置 0 表示关闭自动提交，改由 Run 在处理成功后再手动提交，
// 从而获得「至少一次」的投递保证。
func NewConsumer(brokers []string, topic, group string, handler Handler) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			Topic:          topic,
			GroupID:        group,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			MaxWait:        time.Second,
			CommitInterval: 0,
			StartOffset:    kafka.FirstOffset,
		}),
		handler: handler,
	}
}

// Run 持续拉取并处理消息，直到 ctx 被取消。
//
// 处理失败的后果是「位移不提交」：本次不重放，但在 Worker 重启或分区再均衡后
// 该消息会重新投递，配合处理逻辑的幂等性实现至少一次语义。
func (c *Consumer) Run(ctx context.Context) error {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("拉取消息失败: %w", err)
		}

		if err := c.handler(ctx, msg.Value); err != nil {
			logger.L().Error("消息处理失败，跳过并稍后重投",
				zap.String("topic", msg.Topic),
				zap.Int("partition", msg.Partition),
				zap.Int64("offset", msg.Offset),
				zap.Error(err),
			)
			// 简单退避，避免下游持续故障时把 CPU 打满
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			return fmt.Errorf("提交位移失败: %w", err)
		}
	}
}

// Close 关闭消费者。
func (c *Consumer) Close() error {
	return c.reader.Close()
}
