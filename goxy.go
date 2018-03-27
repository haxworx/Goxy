package main

import "fmt"
import "os"
import "log"
import "strings"
import "strconv"
import "time"
import "io"
import "bufio"
import "net"
import "net/http"
import "crypto/tls"

const (
	CONNECTED = iota
	DISCONNECTED
)

const CERT_FILE = "config/server.crt"
const CERT_KEY_FILE = "config/server.key"
const CLIENTS_MAX = 32

func transfer(src io.ReadCloser, dst io.WriteCloser, ch chan int) {
	defer src.Close()
	defer dst.Close()

	io.Copy(dst, src)

	ch <- 1
}

func responseWrite(conn net.Conn, resp *http.Response) {
	var buf = make([]byte, 4096)
	for {
		bytes, err := resp.Body.Read(buf)
		conn.Write(buf[:bytes])
		if err != nil {
			if err == io.EOF {
				break
			} else {
				break
			}
		}
	}
}

type Client struct {
	conn    net.Conn
	req     *http.Request
	url     string
	address string
	state   int
}

type Proxy struct {
	listener net.Listener
	address  string
	ch       chan Client
	clients  []Client
}

func NewProxy(hostname string, port int) Proxy {
	address := fmt.Sprintf("%s:%d", hostname, port)
	ch := make(chan Client, CLIENTS_MAX)

	return Proxy{address: address, ch: ch}
}

func (p *Proxy) ListenAndServe() error {
	var err error

	if p.listener == nil {
		p.listener, err = net.Listen("tcp", p.address)
		if err != nil {
			os.Exit(1 << 1)
		}
	}

	conn, err := p.listener.Accept()
	if err != nil {
		return err
	}

	go p.ClientConnect(conn)

	return nil
}

func (p *Proxy) Get(client *Client) {

	resp, err := http.Get(client.url)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	responseWrite(client.conn, resp)
}

func (p *Proxy) Post(client *Client) {

	resp, err := http.Post(client.url, client.req.Header.Get("Content-Type"), client.req.Body)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	responseWrite(client.conn, resp)
}

func (p *Proxy) Connect(client *Client) {

	address := client.url[1+strings.LastIndex(client.url, "/"):]
	conn, err := net.DialTimeout("tcp", address, 2 * time.Second)
	if err != nil {
		return
	}

	client.conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))

	ch := make(chan int)

	go transfer(conn, client.conn, ch)
	go transfer(client.conn, conn, ch)

	for i := 0; i < 2; i++ {
		<-ch
	}
}

func (p *Proxy) Options(client *Client) {

	allowed := "GET,POST,CONNECT,OPTIONS"
	reply := fmt.Sprintf("HTTP/1.1 200 OK\r\nAllow: %s\r\n\r\n", allowed)
	client.conn.Write([]byte(reply))
}

func (p *Proxy) Request(client *Client) (*http.Request, error) {

	r := bufio.NewReader(client.conn)
	req, err := http.ReadRequest(r)
	if err != nil {
		return nil, err
	}

	client.url = req.URL.String()
	client.req = req

	return req, nil
}

func (p *Proxy) ClientConnect(conn net.Conn) {

	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(20 * time.Second))

	client := &Client{conn: conn, address: conn.RemoteAddr().String(), state: CONNECTED}

	req, err := p.Request(client)
	if err != nil {
		return
	}

	p.ch <- *client

	switch req.Method {
	case "OPTIONS":
		p.Options(client)
	case "GET":
		p.Get(client)
	case "POST":
		p.Post(client)
	case "CONNECT":
		p.Connect(client)
	case "disablethisCONNECT":
		manInTheMiddle(client)
	}

	client.state = DISCONNECTED

	p.ch <- *client
}

func manInTheMiddle(client *Client) {
	address := client.url[1+strings.LastIndex(client.url, "/"):]
	host, _, _ := net.SplitHostPort(address)
	cert, err := tls.LoadX509KeyPair(CERT_FILE, CERT_KEY_FILE)
	if err != nil {
		log.Fatal("tls.LoadX509KeyPair")
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   host,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}

	/* Upgrade connection to TLS */
	tlsConn := tls.Server(client.conn, tlsConfig)
	defer tlsConn.Close()

	/* Negotiate a TLS session */
	client.conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	tlsConn.Handshake()

	/* Get the HTTP request over TLS from the client*/
	r := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}

	/* Request URL to use on behalf of the client */
	url := fmt.Sprintf("https://%s%s", req.Host, req.URL.String())

	tlsConfig.ServerName = req.Host
	tr := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	https := &http.Client{Transport: tr}

	var resp *http.Response

	/* Make a new HTTPS request on behalf of the client */
	switch req.Method {
	case "GET":
		resp, err = https.Get(url)
		if err != nil {
			return
		}
	case "POST":
		resp, err = https.Post(url, req.Header.Get("Content-Type"), req.Body)
		if err != nil {
			return
		}
	}

	defer resp.Body.Close()

	/* Write the HTTPS response to the upgraded TLS client */
	responseWrite(tlsConn, resp)
}

func (p *Proxy) ClientAppend(client Client) {
	p.clients = append(p.clients, client)
}

func (p *Proxy) ClientRemove(client Client) {
	for i, c := range p.clients {
		if c.address == client.address {
			p.clients[i] = p.clients[len(p.clients)-1]
			p.clients = p.clients[:len(p.clients)-1]
			return
		}
	}
}

func (p *Proxy) Counter(counter chan int) {
	for {
		select {
		case client := <-p.ch:
			if client.state == CONNECTED {
				p.ClientAppend(client)
			} else if client.state == DISCONNECTED {
				p.ClientRemove(client)
			}
		default:
			counter <- len(p.clients)
		}
	}
}

func (p *Proxy) Run() {
	counter := make(chan int)

	go p.Counter(counter)

	for {
		count := <-counter
		if count < CLIENTS_MAX {
			err := p.ListenAndServe()
			if err != nil {
				os.Exit(1 << 0)
			}
		}
	}
}

func Help() {
	fmt.Println("Usage: goxy [OPTIONS] <hostname> <port>\n" +
		"   Where OPTIONS can be a combination of\n" +
		"   -h | --help\n" +
		"     This help.")
	os.Exit(0)
}

func main() {
	argc := len(os.Args)

	if argc < 3 {
		Help()
	}

	for i := 1; i < argc; i++ {
		if os.Args[i] == "-h" || os.Args[i] == "--help" {
			Help()
		}
	}

	port, _ := strconv.Atoi(os.Args[2])

	proxy := NewProxy(os.Args[1], port)

	fmt.Printf("listening on %s.\n", proxy.address)

        proxy.Run()
}
