// fakenode - a test stand-in for the ESP32 rproxy firmware. It speaks the
// identical pool protocol (HELLO/READY, DIAL/OK/ERR, relay) so we can test the
// broker + GOST chain end to end without hardware. It also applies the same
// private-IP / blocked-port safety filter as the firmware.
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func blockedIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
	}
	if ip4[0] == 0 || ip4[0] == 127 {
		return true
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return true
	}
	if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true // CGNAT
	}
	return false
}

func blockedPort(p int) bool { return p == 0 || p == 25 || p == 465 || p == 587 }

// allowLocal disables the private-IP filter for loopback reachability testing
// in the sandbox (set ALLOW_LOCAL=1). Never set in production.
var allowLocal = os.Getenv("ALLOW_LOCAL") == "1"

func worker(id int, broker, token string) {
	backoff := time.Second
	for {
		c, err := net.DialTimeout("tcp", broker, 10*time.Second)
		if err != nil {
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		br := bufio.NewReader(c)
		fmt.Fprintf(c, "HELLO %s\n", token)
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY" {
			c.Close()
			continue
		}
		// wait for one DIAL
		line, err = br.ReadString('\n')
		if err != nil {
			c.Close()
			continue
		}
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 3 || f[0] != "DIAL" {
			c.Write([]byte("ERR badreq\n"))
			c.Close()
			continue
		}
		host, portS := f[1], f[2]
		port, _ := strconv.Atoi(portS)
		if blockedPort(port) {
			c.Write([]byte("ERR blocked\n"))
			c.Close()
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			c.Write([]byte("ERR dial\n"))
			c.Close()
			continue
		}
		if !allowLocal && blockedIP(ips[0]) {
			c.Write([]byte("ERR blocked\n"))
			c.Close()
			continue
		}
		tc, err := net.DialTimeout("tcp", net.JoinHostPort(host, portS), 10*time.Second)
		if err != nil {
			c.Write([]byte("ERR dial\n"))
			c.Close()
			continue
		}
		c.Write([]byte("OK\n"))
		log.Printf("w%d dial %s:%d", id, host, port)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			io.Copy(tc, br)
			if t, ok := tc.(*net.TCPConn); ok {
				t.CloseWrite()
			}
		}()
		go func() {
			defer wg.Done()
			io.Copy(c, tc)
			if t, ok := c.(*net.TCPConn); ok {
				t.CloseWrite()
			}
		}()
		wg.Wait()
		tc.Close()
		c.Close()
	}
}

func main() {
	broker := os.Getenv("BROKER")
	if broker == "" {
		broker = "127.0.0.1:7000"
	}
	token := os.Getenv("TOKEN")
	n := 4
	if v := os.Getenv("POOL"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	for i := 0; i < n; i++ {
		go worker(i, broker, token)
	}
	log.Printf("fakenode: %d workers -> %s", n, broker)
	select {}
}
