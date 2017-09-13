package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/codegangsta/cli"
)

const (
	serverConfAppName     = "deis-builder-server"
	gitReceiveConfAppName = "deis-builder-git-receive"
	gitHomeDir            = "/Users/smothiki/git"
)

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func main() {

	app := cli.NewApp()

	app.Commands = []cli.Command{
		{
			Name:    "server",
			Aliases: []string{"srv"},
			Usage:   "Run the git server",
			Action: func(c *cli.Context) {

				cfg, err := Configure()
				if err != nil {
					fmt.Printf("config error%v\n", err)
				}
				err = Serve(cfg, gitHomeDir, "localhost:2223", "gitreceive")
				if err != nil {
					fmt.Println(err)
				}
				fmt.Println("here")
				for {
				}
			},
		},
		{
			Name:    "git-receive",
			Aliases: []string{"gr"},
			Usage:   "Run the git-receive hook",
			Action: func(c *cli.Context) {

				// if err := gitreceive.Run(cnf); err != nil {
				// 	log.Printf("Error running git receive hook [%s]", err)
				// 	os.Exit(1)
			},
		},
	}

	app.Run(os.Args)
}
