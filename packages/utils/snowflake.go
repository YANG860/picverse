package utils

import (
	"fmt"
	"rednote/config"

	"github.com/bwmarrin/snowflake"
)

var Node *snowflake.Node

func init() {
	err := InitSnowflake(config.Global.SnowFlake.ID)
	if err != nil {
		panic(fmt.Errorf("Failed to initialize Snowflake: %v", err))
	}
}

// InitSnowflake 初始化雪花节点，machineID 范围 0-1023
// 初始化失败时保留原节点不变，避免破坏调用方正在使用的全局 Node
func InitSnowflake(machineID int64) error {
	node, err := snowflake.NewNode(machineID)
	if err != nil {
		return fmt.Errorf("初始化雪花节点失败: %w", err)
	}
	Node = node
	return nil
}

func GenID() snowflake.ID {
	return Node.Generate()
}
