module agent-coordinator

go 1.21

require (
	github.com/gorilla/websocket v1.5.3
	representable v0.0.0
	ufa-loader v0.0.0
	ufa-version v0.0.0
)

replace representable => ../representable

replace ufa-loader => ../ufa-loader

replace ufa-version => ../ufa-version
