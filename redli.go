package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gomodule/redigo/redis"
	"github.com/mattn/go-shellwords"
	"github.com/peterh/liner"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	debug         = kingpin.Flag("debug", "Enable debug mode.").Bool()
	longprompt    = kingpin.Flag("long", "Enable long prompt with host/port").Bool()
	redisurl      = kingpin.Flag("uri", "URI to connect to").Short('u').URL()
	redishost     = kingpin.Flag("host", "Host to connect to").Short('h').Default("127.0.0.1").String()
	redisport     = kingpin.Flag("port", "Port to connect to").Short('p').Default("6379").Int()
	redisauth     = kingpin.Flag("auth", "Password to use when connecting").Short('a').String()
	redisdb       = kingpin.Flag("ndb", "Redis database to access").Short('n').Default("0").Int()
	redistls      = kingpin.Flag("tls", "Enable TLS/SSL").Default("false").Bool()
	rediscertfile = kingpin.Flag("certfile", "Self-signed certificate file for validation").Envar("REDIS_CERTFILE").File()
	rediscertb64  = kingpin.Flag("certb64", "Self-signed certificate string as base64 for validation").Envar("REDIS_CERTB64").String()
	commandargs   = kingpin.Arg("commands", "Redis commands and values").Strings()
)

var (
	rawrediscommands = Commands{}
	conn             redis.Conn
)

func main() {
	kingpin.Parse()

	cert := []byte{}

	if *rediscertfile != nil {
		mycert, err := ioutil.ReadAll(*rediscertfile)
		if err != nil {
			log.Fatal(err)
		}
		cert = mycert
	} else if rediscertb64 != nil {
		mycert, err := base64.StdEncoding.DecodeString((*rediscertb64))
		if err != nil {
			log.Fatal("What", err)
		}
		cert = mycert
	}

	connectionurl := ""

	if *redisurl == nil {
		// With no URI, build a URI from other flags
		if *redistls {
			connectionurl = "rediss://"
		} else {
			connectionurl = "redis://"
		}

		if redisauth != nil {
			connectionurl = connectionurl + "x:" + *redisauth + "@"
		}

		connectionurl = connectionurl + *redishost + ":" + strconv.Itoa(*redisport) + "/" + strconv.Itoa(*redisdb)
	} else {
		connectionurl = (*redisurl).String()
	}

	// If we have a certificate, then assume TLS
	if len(cert) > 0 {

		config := &tls.Config{RootCAs: x509.NewCertPool(),
			ClientAuth: tls.RequireAndVerifyClientCert}

		ok := config.RootCAs.AppendCertsFromPEM(cert)
		if !ok {
			log.Fatal("Couldn't load cert data")
		}

		var err error
		conn, err = redis.DialURL(connectionurl, redis.DialTLSConfig(config))
		if err != nil {
			log.Fatal("Dial TLS ", err)
		}
		defer conn.Close()
	} else {
		var err error
		conn, err = redis.DialURL(connectionurl)
		if err != nil {
			log.Fatal("Dial ", err)
		}
		defer conn.Close()
	}

	// We may not need to carry on setting up the interactive front end so...
	if *commandargs != nil {
		command := *commandargs
		var args = make([]interface{}, len(command[1:]))
		for i, d := range command[1:] {
			args[i] = d
		}
		result, err := conn.Do(command[0], args...)

		if err != nil {
			log.Fatal(err)
		}

		switch v := result.(type) {
		case redis.Error:
			fmt.Printf("%s\n", v.Error())
		case int64:
			fmt.Printf("%d\n", v)
		case string:
			fmt.Printf("%s\n", v)
		case []byte:
			fmt.Printf("%s\n", string(v))
		case nil:
			fmt.Printf("nil\n")
		case []interface{}:
			for i, j := range v {
				fmt.Printf("%d) %s\n", i+1, j)
			}
		}

		os.Exit(0)
	}

	json.Unmarshal([]byte(redisCommandsJSON), &rawrediscommands)

	rediscommands := make(map[string]Command, len(rawrediscommands))
	commandstrings := make([]string, len(rawrediscommands))

	i := 0
	for k, v := range rawrediscommands {
		command := strings.ToLower(k)
		commandstrings[i] = command
		i = i + 1
		rediscommands[command] = v
	}

	sort.Strings(commandstrings)

	reply, err := redis.String(conn.Do("INFO"))
	if err != nil {
		log.Fatal(err)
	}

	info := redisParseInfo(reply)

	fmt.Printf("Connected to %s\n", info["redis_version"])

	liner := liner.NewLiner()
	defer liner.Close()

	liner.SetCtrlCAborts(true)

	liner.SetCompleter(func(line string) (c []string) {
		lowerline := strings.ToLower(line)
		for _, n := range commandstrings {
			if strings.HasPrefix(n, lowerline) {
				c = append(c, n)
			}
		}
		if len(c) == 0 {
			if strings.HasPrefix(lowerline, "help ") {
				helpphrase := strings.TrimPrefix(lowerline, "help ")
				for _, n := range commandstrings {
					if strings.HasPrefix(n, helpphrase) {
						c = append(c, "help "+n)
					}
				}
			}
		}
		return
	})

	for {
		line, err := liner.Prompt(getPrompt())
		if err != nil {
			break
		}

		if len(line) == 0 {
			continue // Ignore no input
		}

		parts, err := shellwords.Parse(line)

		if len(parts) == 0 {
			continue // Ignore no input
		}

		liner.AppendHistory(line)

		if parts[0] == "help" {
			if len(parts) == 1 {
				fmt.Println("Enter help <command> to show information about a command")
				continue
			}
			lookup := parts[1]
			if len(parts) == 3 {
				lookup = parts[1] + " " + parts[2]
			}
			commanddata, ok := rediscommands[lookup]
			if ok {
				fmt.Printf("Command: %s\n", strings.ToUpper(lookup))
				fmt.Printf("Summary: %s\n", commanddata.Summary)
				if commanddata.Complexity != "" {
					fmt.Printf("Complexity: %s\n", commanddata.Complexity)
				}
				if commanddata.Arguments != nil {
					fmt.Println("Args:")
					for _, a := range commanddata.Arguments {
						fmt.Printf("     %s (%s)\n", a.Name, a.Type)
					}
				}
				continue
			}

		}

		if parts[0] == "exit" {
			break
		}

		var args = make([]interface{}, len(parts[1:]))
		for i, d := range parts[1:] {
			args[i] = d
		}

		result, err := conn.Do(parts[0], args...)

		switch v := result.(type) {
		case redis.Error:
			fmt.Printf("%s\n", v.Error())
		case int64:
			fmt.Printf("%d\n", v)
		case string:
			fmt.Printf("%s\n", v)
		case []byte:
			fmt.Printf("%s\n", string(v))
		case nil:
			fmt.Printf("nil\n")
		case []interface{}:
			for i, j := range v {
				fmt.Printf("%d) %s\n", i+1, j)
			}
		}
	}
}

func redisParseInfo(reply string) map[string]string {
	lines := strings.Split(reply, "\r\n")
	values := map[string]string{}
	for _, line := range lines {
		if len(line) > 0 && line[0] != '#' {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				values[parts[0]] = parts[1]
			}
		}
	}
	return values
}

func getPrompt() string {
	if *longprompt {
		return fmt.Sprintf("%s:%s> ", (*redisurl).Hostname(), (*redisurl).Port())
	}

	return "> "
}

func printAsJSON(toprint interface{}) {
	jsonstr, _ := json.MarshalIndent(toprint, "", " ")
	fmt.Println(string(jsonstr))
}

//Commands is a holder for Redis Command structures
type Commands map[string]Command

//Command is a holder for Redis Command data includint arguments
type Command struct {
	Summary    string     `json:"summary"`
	Complexity string     `json:"complexity"`
	Arguments  []Argument `json:"arguments"`
	Since      string     `json:"since"`
	Group      string     `json:"group"`
}

//Argument is a holder for Redis Command Argument data
type Argument struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Enum     string `json:"enum,omitempty"`
	Optional bool   `json:"optional"`
}
