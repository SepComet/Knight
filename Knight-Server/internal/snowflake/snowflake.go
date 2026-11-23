// Package snowflake 雪花 ID 生成器（Twitter Snowflake 64 位布局）。
//
// 布局：1 符号位 | 41 位毫秒时间戳（自定义纪元起，约 69 年）| 10 位 worker | 12 位序列（每毫秒 4096）。
//
// 时钟回拨防护：服务端部署形态为 Docker Desktop（WSL2），容器时钟跟随
// Windows 宿主机（容器无 CAP_SYS_TIME，无法自校时），宿主机睡眠恢复 / NTP
// 校正会使时钟向后跳变。生成时若 now < lastTS，自旋等待时钟追上；回拨超过
// MaxRollback 则拒绝生成，宁可报错也不产出重复 ID（撞主键）。
package snowflake

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// 纪元：2026-01-01 00:00:00 UTC（项目元年；41 位时间戳可用至约 2095 年）。
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	workerBits  = 10
	seqBits     = 12
	maxWorkerID = (1 << workerBits) - 1 // 1023
	seqMask     = (1 << seqBits) - 1   // 4095

	// MaxRollback 允许等待的时钟回拨上限；超过视为时钟异常，拒绝生成。
	MaxRollback = 2 * time.Second
)

// ErrClockRollback 时钟回拨超过可等待上限，拒绝生成以防重号。
var ErrClockRollback = errors.New("snowflake: clock rollback beyond max wait")

// Node 雪花 ID 生成器，并发安全。
type Node struct {
	mu      sync.Mutex
	worker  uint64
	lastTS  uint64 // 上次生成时间的毫秒值（相对 epoch）
	seq     uint64
	nowFunc func() time.Time // 可注入测试时钟
}

// NewNode 按 workerID（0~1023）构造。
// 多实例部署时各实例必须使用不同 workerID（config.KNIGHT_SNOWFLAKE_WORKER_ID）。
func NewNode(workerID int) (*Node, error) {
	if workerID < 0 || workerID > maxWorkerID {
		return nil, fmt.Errorf("snowflake: worker id %d out of range [0, %d]", workerID, maxWorkerID)
	}
	return &Node{worker: uint64(workerID), nowFunc: time.Now}, nil
}

// Next 生成一个全局唯一 ID。单节点内严格递增（同毫秒序列递增，跨毫秒时间递增）。
func (n *Node) Next() (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := n.nowMS()
	if now < n.lastTS {
		if time.Duration(n.lastTS-now)*time.Millisecond > MaxRollback {
			return 0, fmt.Errorf("%w: back=%dms", ErrClockRollback, n.lastTS-now)
		}
		// 等待真实时钟追上回拨前的时间，不做前向补偿（不挪用未来时间）
		for now < n.lastTS {
			time.Sleep(time.Millisecond)
			now = n.nowMS()
		}
	}

	if now == n.lastTS {
		n.seq = (n.seq + 1) & seqMask
		if n.seq == 0 {
			// 当前毫秒序列耗尽：等待进入下一毫秒
			for now <= n.lastTS {
				time.Sleep(time.Millisecond)
				now = n.nowMS()
			}
		}
	} else {
		n.seq = 0
	}
	n.lastTS = now
	return now<<(workerBits+seqBits) | n.worker<<seqBits | n.seq, nil
}

func (n *Node) nowMS() uint64 {
	d := n.nowFunc().Sub(epoch)
	if d < 0 {
		// 时钟早于纪元（被重置/电池耗尽等）：按 0 处理，走回拨防护路径
		return 0
	}
	return uint64(d / time.Millisecond)
}
