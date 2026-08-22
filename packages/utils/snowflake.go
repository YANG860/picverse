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
func InitSnowflake(machineID int64) error {
	var err error
	Node, err = snowflake.NewNode(machineID)
	if err != nil {
		return fmt.Errorf("初始化雪花节点失败: %w", err)
	}
	return nil
}

func GenID() snowflake.ID {
	return Node.Generate()
}
