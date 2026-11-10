module dtt

go 1.25

require (
	dungeon-keeper v0.0.0
	ufa-configurable v0.0.0
	ufa-version v0.0.0
)

require github.com/google/uuid v1.6.0 // indirect

replace dungeon-keeper => ../dungeon-keeper

replace representable => ../representable

replace ufa-configurable => ../ufa-configurable

replace ufa-hostid => ../ufa-hostid

replace ufa-loader => ../ufa-loader

replace ufa-version => ../ufa-version
