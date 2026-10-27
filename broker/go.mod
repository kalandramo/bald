module github.com/kalandramo/bald/broker

go 1.27.1

require github.com/kalandramo/bald/encoding v0.1.0

replace github.com/kalandramo/bald/encoding => ../encoding

replace github.com/kalandramo/bald/encoding/json => ../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../encoding/proto
