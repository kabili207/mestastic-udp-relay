package connectors

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	pb "github.com/meshnet-gophers/meshtastic-go/meshtastic"
	"github.com/rs/zerolog"
	slogzerolog "github.com/samber/slog-zerolog"
	"google.golang.org/protobuf/proto"
)

type MqttConnector struct {
	server    string
	username  string
	password  string
	topicRoot string
	clientID  string
	client    mqtt.Client
	log       *slog.Logger
	sync.RWMutex
	packetHandler       MeshPacketHandler
	stateFunc           StateEventHandler
	previouslyConnected bool
}

type ConnectionLostHandler func(error)

type OnConnectHandler func()

type ReconnectHandler func()

func NewMqttConnector(url, username, password, rootTopic string, logger zerolog.Logger) MeshConnector {
	slogger := slog.New(slogzerolog.Option{Level: slog.LevelInfo, Logger: &logger}.NewZerologHandler())

	return &MqttConnector{
		server:    url,
		username:  username,
		password:  password,
		topicRoot: rootTopic,
		log:       slogger,
	}
}

func (c *MqttConnector) TopicRoot() string {
	return c.topicRoot
}

func randomString(n int, letters []rune) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}

func (h *MqttConnector) Name() string {
	return "MQTT"
}

func (c *MqttConnector) Start() error {
	var alphabet = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	c.clientID = fmt.Sprintf("%s-%s", c.username, randomString(6, alphabet))

	handler := c.log.Handler()

	mqtt.DEBUG = slog.NewLogLogger(handler, slog.LevelDebug)
	mqtt.WARN = slog.NewLogLogger(handler, slog.LevelWarn)
	mqtt.ERROR = slog.NewLogLogger(handler, slog.LevelError)
	mqtt.CRITICAL = slog.NewLogLogger(handler, slog.LevelError+4)

	opts := mqtt.NewClientOptions().
		AddBroker(c.server).
		SetUsername(c.username).
		SetOrderMatters(false).
		SetPassword(c.password).
		SetClientID(c.clientID).
		SetCleanSession(false)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetResumeSubs(true)
	//opts.SetDefaultPublishHandler(f)
	opts.SetPingTimeout(5 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(1 * time.Minute)
	opts.SetConnectionLostHandler(c.onConnectionLost)
	opts.SetReconnectingHandler(c.onReconnecting)
	opts.SetOnConnectHandler(c.onConnected)
	opts.SetTLSConfig(&tls.Config{
		InsecureSkipVerify: true,
	})
	c.client = mqtt.NewClient(opts)
	if token := c.client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	c.client.Subscribe(c.topicRoot+"/+", 0, c.handleBrokerMessage)
	return nil
}

func (c *MqttConnector) Stop() {
	if c.client != nil {
		c.client.Disconnect(1000)
	}
}

func (c *MqttConnector) IsConnected() bool {
	return c.client != nil && c.client.IsConnected()
}

// MQTT Message
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

// Publish a message to the broker
func (c *MqttConnector) Publish(m *Message) error {
	tok := c.client.Publish(m.Topic, 0, m.Retained, m.Payload)
	if !tok.WaitTimeout(10 * time.Second) {
		tok.Wait()
		return errors.New("timeout on mqtt publish")
	}
	if tok.Error() != nil {
		return tok.Error()
	}
	return nil
}

// Register a handler for messages on the specified channel
func (c *MqttConnector) SetPacketHandler(h MeshPacketHandler) {
	c.Lock()
	defer c.Unlock()
	c.packetHandler = h
}

func (c *MqttConnector) handleBrokerMessage(client mqtt.Client, message mqtt.Message) {
	msg := Message{
		Topic:    message.Topic(),
		Payload:  message.Payload(),
		Retained: message.Retained(),
	}
	c.RLock()
	defer c.RUnlock()

	var env pb.MeshPacket
	if err := proto.Unmarshal(msg.Payload, &env); err != nil {
		c.log.Error("Error unmarshalling packet", "error", err)
		return
	}

	if c.packetHandler != nil {
		go c.packetHandler(c, &env)
	}
}

func (h *MqttConnector) SendPacket(packet *pb.MeshPacket) error {

	rawEnv, err := proto.Marshal(packet)
	if err != nil {
		return err
	}

	reply := Message{
		Topic:   fmt.Sprintf("%s/%s", h.topicRoot, h.clientID),
		Payload: rawEnv,
	}

	return h.Publish(&reply)
}

func (c *MqttConnector) SetLogger(logger *slog.Logger) {
	if logger == nil {
		c.log = slog.Default()
	} else {
		c.log = logger
	}
}

// SetStateHandler implements MeshHandler.
func (h *MqttConnector) SetStateHandler(fn StateEventHandler) {
	h.stateFunc = fn
}

func (c *MqttConnector) onConnected(client mqtt.Client) {
	if c.stateFunc != nil {
		if c.previouslyConnected {
			c.stateFunc(c, EventRestarted)
		} else {
			c.log.Info("MQTT connector started", "client_id", c.clientID)
			c.previouslyConnected = true
			c.stateFunc(c, EventStarted)
		}
	}
}

func (c *MqttConnector) onConnectionLost(client mqtt.Client, err error) {
	if c.stateFunc != nil {
		c.stateFunc(c, EventConnectionLost)
	}
}

func (c *MqttConnector) onReconnecting(client mqtt.Client, options *mqtt.ClientOptions) {
	c.log.Info("mqtt reconnecting")
}
