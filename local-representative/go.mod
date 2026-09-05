module local-representative

go 1.21

require (
	github.com/gorilla/websocket v1.5.3
	representable v0.0.0
	ufa-configurable v0.0.0
)

replace representable => ../representable

replace ufa-configurable => ../ufa-configurable
