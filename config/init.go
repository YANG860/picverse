package config

import (
	"github.com/spf13/viper"
)

func Init(path string) *Config {
	viper.AddConfigPath(path)
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	err := viper.ReadInConfig()
	if err != nil {
		panic(err)
	}
	var cfg Config
	err = viper.Unmarshal(&cfg)
	if err != nil {
		panic(err)
	}
	Global = &cfg
	return &cfg
}
