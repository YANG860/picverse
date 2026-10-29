// Command dbtool 清空测试数据库基线（e2e/排查前归零用）：
// TRUNCATE 全部业务表 + Purge JetStream 流 + 删除 durable 消费者。
//
// 注意：必须同时删除 durable 消费者——仅 Purge 流会使活跃消费者的状态与流序号
// 错位，新消息被误判为已交付而跳过（表现为"发 N 消 <N"的计数漂移假象）。
// 跑本命令后需重启 consumer/poller 进程（以全新订阅状态重建消费者）。
//
// 用法：
//
//	go run ./scripts/dbtool -config ./config/
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"rednote/config"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	cfgPath := flag.String("config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(*cfgPath)

	// ---- PG：TRUNCATE 全部业务表（动态查表，含 auth/outbox/关注等未建模表名）----
	db, err := gorm.Open(postgres.Open(cfg.Database.PG.URL), &gorm.Config{})
	if err != nil {
		log.Fatalf("连接 PG 失败: %v", err)
	}
	var tables []string
	if err := db.Raw(`SELECT tablename FROM pg_tables WHERE schemaname='public'`).Scan(&tables).Error; err != nil {
		log.Fatalf("查询表失败: %v", err)
	}
	if len(tables) == 0 {
		log.Println("public 无表，跳过")
	} else {
		trunc := "TRUNCATE TABLE"
		for i, t := range tables {
			if i > 0 {
				trunc += ","
			}
			trunc += " " + t
		}
		trunc += " CASCADE"
		if err := db.Exec(trunc).Error; err != nil {
			log.Fatalf("TRUNCATE 失败: %v", err)
		}
		fmt.Printf("已清空表: %v\n", tables)
	}

	// ---- JetStream：purge 流残留消息 + 删除 durable 消费者（消费状态一并归零）----
	nc, err := nats.Connect(cfg.JetStream.Url)
	if err != nil {
		log.Fatalf("连接 NATS 失败: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("创建 JetStream 失败: %v", err)
	}
	for _, stream := range []string{"media", "post"} {
		st, err := js.Stream(context.Background(), stream)
		if err != nil {
			log.Printf("stream %s 不存在，跳过: %v", stream, err)
			continue
		}
		if err := st.Purge(context.Background()); err != nil {
			log.Printf("purge stream %s 失败: %v", stream, err)
			continue
		}
		// 删除 durable 消费者：Purge 后活跃消费者的状态与流序号错位，
		// 新消息可能被误判为已交付而跳过（消费端状态必须一并归零）
		if err := st.DeleteConsumer(context.Background(), stream+"_consumer"); err != nil {
			log.Printf("delete consumer %s_consumer 失败（可能不存在，跳过）: %v", stream, err)
		}
		fmt.Printf("已清空 stream + consumer: %s\n", stream)
	}
	fmt.Println("完成")
}
