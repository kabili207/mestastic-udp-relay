package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jellydator/ttlcache/v3"
	connectors "github.com/kabili207/meshtastic-udp-relay/internal"
	"github.com/manifoldco/promptui"
	pb "github.com/meshnet-gophers/meshtastic-go/meshtastic"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

var (
	meshConnectors []connectors.MeshConnector

	packetCache     *ttlcache.Cache[uint64, any]
	packetCacheLock sync.Mutex

	logger zerolog.Logger
)

type Configuration struct {
	Broker    string `yaml:"broker"`
	Username  string `yaml:"user"`
	Password  string `yaml:"password"`
	RootTopic string `yaml:"root_topic"`
}

func buildConfigPrompt() Configuration {
	validateBroker := func(input string) error {

		if !strings.Contains(input, "://") {
			input = "tcp://" + input
		}
		brokerURI, err := url.Parse(input)
		if err != nil {
			return err
		}
		if brokerURI.Scheme != "tcp" && brokerURI.Scheme != "ssl" && brokerURI.Scheme != "ws" {
			return errors.New("broker scheme must be one of tcp, ssl, or ws")
		}
		return nil
	}

	prompt := promptui.Prompt{
		Label:    "MQTT Broker",
		Validate: validateBroker,
		Default:  "ssl://mqtt.wpamesh.net:8883",
	}

	config := Configuration{}
	result, err := prompt.Run()

	for {
		if err == nil {
			break
		}
		fmt.Printf("Invalid broker: %v", err)
		result, err = prompt.Run()
	}
	config.Broker = result

	prompt = promptui.Prompt{
		Label: "MQTT User",
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("must not be empty")
			}
			return nil
		},
	}
	result, err = prompt.Run()

	for {
		if err == nil {
			break
		}
		fmt.Printf("Invalid user: %v", err)
		result, err = prompt.Run()
	}
	config.Username = result

	prompt = promptui.Prompt{
		Label: "MQTT Password",
		Mask:  '*',
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("must not be empty")
			}
			return nil
		},
	}
	result, err = prompt.Run()

	for {
		if err == nil {
			break
		}
		fmt.Printf("Invalid password: %v", err)
		result, err = prompt.Run()
	}
	config.Password = result

	prompt = promptui.Prompt{
		Label:   "MQTT Root Topic",
		Default: "mesht/relay",
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("must not be empty")
			}
			return nil
		},
	}
	result, err = prompt.Run()

	for {
		if err == nil {
			break
		}
		fmt.Printf("Invalid root topic: %v", err)
		result, err = prompt.Run()
	}
	config.RootTopic = result

	return config
}

func loadConfig() Configuration {
	yamlFile, err := os.ReadFile("config.yml")
	var config Configuration
	if err != nil {
		fmt.Println("Unable to load config")
		config = buildConfigPrompt()
		out, err := yaml.Marshal(config)
		if err != nil {
			log.Fatal(err)
		}
		f, err := os.Create("config.yml")
		if err != nil {
			log.Fatalf("Error creating config file\n%v", err)
		}
		defer f.Close()
		if _, err := f.Write(out); err != nil {
			log.Fatalf("Error creating config file\n%v", err)
		}
		return config
	}

	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}
	return config
}

func main() {

	logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
		With().Timestamp().Logger()

	packetCache = ttlcache.New(
		ttlcache.WithTTL[uint64, any](2 * time.Hour),
	)

	config := loadConfig()

	meshConnectors = []connectors.MeshConnector{
		connectors.NewUDPMessageHandler(logger),
		connectors.NewMqttConnector(config.Broker, config.Username, config.Password, config.RootTopic, logger),
	}

	for _, c := range meshConnectors {
		c.SetPacketHandler(handlePacket)
		c.SetStateHandler(handleConnectorStateChange)
	}

	for _, c := range meshConnectors {
		if err := c.Start(); err != nil {
			logger.Fatal().Err(err).Msg("Error starting connector")
		}
	}

	// Setup signal handler
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)

	tickerTele := time.NewTicker(15 * time.Minute)

loopStart:
	for {
		select {
		case <-tickerTele.C:
			packetCacheLock.Lock()
			packetCache.DeleteExpired()
			packetCacheLock.Unlock()
		case sig := <-sigc:
			logger.Printf("got signal %v", sig)
			break loopStart
		}
	}

	logger.Printf("shutting down")

	for _, c := range meshConnectors {
		c.Stop()
	}
}

func isDuplicatePacket(packet *pb.MeshPacket) bool {
	if packet.Id == 0 {
		return true
	}
	cacheKey := (uint64(packet.From) << 32) | uint64(packet.Id)
	return packetCache.Has(cacheKey)
}

func cachePacket(packet *pb.MeshPacket) {
	cacheKey := (uint64(packet.From) << 32) | uint64(packet.Id)
	packetCache.Set(cacheKey, nil, ttlcache.DefaultTTL)
}

func handlePacket(conn connectors.MeshConnector, packet *pb.MeshPacket) {

	packetCacheLock.Lock()
	if isDuplicatePacket(packet) {
		packetCacheLock.Unlock()
		return
	}
	cachePacket(packet)
	packetCacheLock.Unlock()

	logger.Debug().
		Uint32("packet_id", packet.Id).
		Str("node_from", fmt.Sprintf("!%08x", packet.From)).
		Str("node_to", fmt.Sprintf("!%08x", packet.To)).
		Uint32("hops_remaining", packet.HopLimit).
		Str("received_via", conn.Name()).
		Msg("Relaying packet")

	for _, c := range meshConnectors {
		if c == conn {
			continue
		}
		if !c.IsConnected() {
			logger.Warn().Str("connector", c.Name()).Msg("Skipping offline connector")
			continue
		}
		if err := c.SendPacket(packet); err != nil {
			logger.Err(err).
				Str("connector", c.Name()).
				Msg("Error relaying packet")
		}
	}
}

func handleConnectorStateChange(mh connectors.MeshConnector, le connectors.ListenerEvent) {

	// Count how many other handlers are still connected
	connectedCount := 0
	for _, h := range meshConnectors {
		if h.IsConnected() {
			connectedCount++
		}
	}

	if connectedCount != len(meshConnectors) {
		//logger.Warn().Msg("Relay not fully connected")
		return
	} else {
		logger.Info().Msg("Relay fully connected")
	}
}
