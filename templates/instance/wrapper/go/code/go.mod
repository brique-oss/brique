module wrapper_main

go 1.22

require brique/wrapper v0.0.0

require golang.org/x/net v0.27.0 // indirect

// The generic runtime is shared by every Go wrapper of the instance and lives
// in <instance>/wrapper/go/lib. This path is relative to this directory and
// assumes <instance>/root/code/<wrapper_name>/; add one "../" per extra
// context level between the instance root and the boundary context.
replace brique/wrapper => ../../../wrapper/go/lib
