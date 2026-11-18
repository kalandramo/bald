// failure.go 是熔断失败口径的单一来源（J2）：MarkFailure 记什么由契约裁定，
// 统一链中间件（pkg/middleware）与节点熔断装饰（selector/circuitbreaker）
// 都从这里取判据。此前节点装饰侧把任何错误（含 4xx/Canceled）都记失败——
// 健康节点被业务 4xx 硬排除（AR-18）；统一链侧有独立的一套判定，两处漂移。

package circuitbreaker

import (
	"context"
	"errors"

	"github.com/kalandramo/bald/berrors"
)

// IsFailureCode 报告一个 berrors 语义码是否算「服务侧失败」。只列服务侧的错：
// 客户端侧的（InvalidArgument / NotFound / PermissionDenied / FailedPrecondition /
// Aborted / OutOfRange / Unauthenticated）是调用方的错，计入失败率会让一个坏
// 客户端替全体用户开窗。ResourceExhausted 也**不在**集合里，尽管字面像
// 「服务满了」：那是限流层自己给出的答案，计入失败等于让熔断消费自己的拒流
// ——配额打满时熔断跟着开窗，两层从互补变成互相放大。
func IsFailureCode(code uint32) bool {
	switch code {
	case berrors.CodeInternal, berrors.CodeUnknown, berrors.CodeUnavailable,
		berrors.CodeDeadlineExceeded, berrors.CodeDataLoss:
		return true
	}
	return false
}

// IsFailureError 是熔断失败口径的完整分类（分类顺序对齐 grpcerr.ToStatus——
// 调用方看到的错误可能是任何形态）：
//  1. *berrors.Error → 用它的 Code（统一链上的错误主形态：桥面 5xx 哨兵、
//     recovery 的 Internal、内层件的拒绝都在此列）；
//  2. context 的两个哨兵 → Canceled 是客户端撤单（不算失败），
//     DeadlineExceeded 是服务没在预算内回来（算失败）；
//  3. 其余原生错误 → 与 grpc-go 一样兜成 Unknown，算失败。
func IsFailureError(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := berrors.FromError(err); ok {
		return IsFailureCode(e.Code)
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}
