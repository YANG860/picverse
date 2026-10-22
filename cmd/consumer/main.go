package main

import (
	"flag"
	"os"
	"os/signal"
	"rednote/cmd/gen"
	"rednote/config"
	"syscall"
)

func main() {

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "./", "path to config file")
	flag.Parse()
	cfg := config.Init(cfgPath)

	mediaConsumer, cleanupMedia := gen.InitializeConsumer(cfg)
	postConsumer, cleanupPost := gen.InitializePostConsumer(cfg)
	mediaConsumer.Start()
	postConsumer.Start()

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGINT, syscall.SIGTERM)
	<-term
	postConsumer.Close()
	mediaConsumer.Close()
	cleanupPost()
	cleanupMedia()
}
