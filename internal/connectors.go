package connectors

import (
	pb "github.com/meshnet-gophers/meshtastic-go/meshtastic"
)

type MeshPacketHandler func(MeshConnector, *pb.MeshPacket)
type StateEventHandler func(MeshConnector, ListenerEvent)

type ListenerEvent int

const (
	EventStarted ListenerEvent = iota
	EventRestarted
	EventConnectionLost
)

type MeshConnector interface {
	Start() error
	Stop()
	Name() string
	IsConnected() bool
	SendPacket(packet *pb.MeshPacket) error
	SetPacketHandler(fn MeshPacketHandler)
	SetStateHandler(fn StateEventHandler)
}
