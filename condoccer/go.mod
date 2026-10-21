module condoccer

go 1.25

require (
	dungeon-keeper v0.0.0
	github.com/gorilla/websocket v1.5.3
	pr-keeper v0.0.0
	representable v0.0.0
	ufa-loader v0.0.0
	ufa-version v0.0.0
)

require github.com/google/uuid v1.6.0 // indirect

replace dungeon-keeper => ../dungeon-keeper

replace pr-keeper => ../pr-keeper

replace representable => ../representable

replace ufa-hostid => ../ufa-hostid

replace ufa-loader => ../ufa-loader

replace ufa-version => ../ufa-version
