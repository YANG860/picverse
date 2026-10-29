// Command jsprobe 探测 JetStream 流与消费者状态（消息丢失/计数漂移排查用）：
// 输出各流的 subjects 与消息数、各 durable 消费者的过滤器/ack 参数/积压统计。
//
// 用法：
//
//	go run ./scripts/jsprobe -config ./config/
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"rednote/config"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	cfgPath := flag.String("config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(*cfgPath)

	nc, err := nats.Connect(cfg.JetStream.Url)
	if err != nil {
		log.Fatalf("连接 NATS 失败: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("创建 JetStream 失败: %v", err)
	}
	ctx := context.Background()

	for _, name := range []string{"media", "post"} {
		st, err := js.Stream(ctx, name)
		if err != nil {
			fmt.Printf("stream %s: 不存在 (%v)\n", name, err)
			continue
		}
		info, err := st.Info(ctx)
		if err != nil {
			log.Fatalf("stream %s info: %v", name, err)
		}
		fmt.Printf("stream %s: subjects=%v msgs=%d\n", name, info.Config.Subjects, info.State.Msgs)

		for _, cname := range []string{name + "_consumer"} {
			cons, err := st.Consumer(ctx, cname)
			if err != nil {
				fmt.Printf("  consumer %s: 不存在 (%v)\n", cname, err)
				continue
			}
			ci, _ := cons.Info(ctx)
			if ci == nil {
				continue
			}
			fmt.Printf("  consumer %s: filter=%q ack_wait=%s max_deliver=%d pending=%d ack_pending=%d\n",
				ci.Name, ci.Config.FilterSubject, ci.Config.AckWait, ci.Config.MaxDeliver,
				ci.NumPending, ci.NumAckPending)
		}
	}
}
