module mcp_server_go

go 1.22

require brique/wrapper v0.0.0

require golang.org/x/net v0.27.0 // indirect

// Shared runtime library: <instance>/wrapper/go/lib (see wrapper/Wrapper.md).
replace brique/wrapper => ../../../../wrapper/go/lib
