module the-conversationalist

go 1.21

require (
	github.com/aws/aws-sdk-go-v2 v1.30.3
	github.com/aws/aws-sdk-go-v2/config v1.27.27
	github.com/aws/aws-sdk-go-v2/service/transcribestreaming v1.9.3
	github.com/gorilla/websocket v1.5.3
	representable v0.0.0
	ufa-loader v0.0.0
	ufa-version v0.0.0
)

replace representable => ../representable

replace ufa-loader => ../ufa-loader

replace ufa-version => ../ufa-version
