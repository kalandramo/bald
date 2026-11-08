module github.com/kalandramo/bald/selector/circuitbreaker

go 1.27.1

require (
	github.com/kalandramo/bald/circuitbreaker v0.2.0
	github.com/kalandramo/bald/circuitbreaker/hystrix v0.1.1
	github.com/kalandramo/bald/selector v0.1.1
)

// 与 selector/balancer 同款：同仓兄弟模块用本地源码路径跟踪（replace 只在
// 本 module 作为主模块时生效，消费者不受影响）。发布随 selector tag 之后走。
replace github.com/kalandramo/bald/circuitbreaker => ../../circuitbreaker

replace github.com/kalandramo/bald/circuitbreaker/hystrix => ../../circuitbreaker/hystrix

replace github.com/kalandramo/bald/selector => ../
