// Command vip is a tiny stand-in for a virtual IP, used only by the local demo.
// Every new connection is forwarded to the port written in $DEMO/target.
package main

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := os.Getenv("DEMO")
	if dir == "" {
		dir = "/tmp/docbrown-demo"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:6432")
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			b, _ := os.ReadFile(filepath.Join(dir, "target"))
			u, err := net.Dial("tcp", "127.0.0.1:"+strings.TrimSpace(string(b)))
			if err != nil {
				return
			}
			defer u.Close()
			go io.Copy(u, c)
			io.Copy(c, u)
		}(c)
	}
}
