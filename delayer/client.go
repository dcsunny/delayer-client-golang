package delayer

import (
	"errors"
	"fmt"
	"time"

	"github.com/gomodule/redigo/redis"
)

// 键名
const (
	KEY_JOB_POOL       = "delayer:job_pool"
	PREFIX_JOB_BUCKET  = "delayer:job_bucket:"
	PREFIX_READY_QUEUE = "delayer:ready_queue:"
)

// 客户端结构
type Client struct {
	Pool            *redis.Pool
	Host            string
	Port            string
	Database        int
	Password        string
	MaxIdle         int
	MaxActive       int
	IdleTimeout     int64
	ConnMaxLifetime int64
}

// 初始化
func (p *Client) Init() error {
	// 创建连接
	pool := &redis.Pool{
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", p.Host+":"+p.Port)
			if err != nil {
				return nil, err
			}
			if p.Password != "" {
				if _, err = c.Do("AUTH", p.Password); err != nil {
					c.Close()
					return nil, err
				}
			}
			if _, err = c.Do("SELECT", p.Database); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		},
		MaxIdle:         p.MaxIdle,
		MaxActive:       p.MaxActive,
		IdleTimeout:     time.Duration(p.IdleTimeout) * time.Second,
		MaxConnLifetime: time.Duration(p.ConnMaxLifetime) * time.Second,
	}
	p.Pool = pool
	return nil
}

// 增加任务
func (p *Client) Push(message Message, delayTime int, readyMaxLifetime int) (bool, error) {
	// 参数验证
	if !message.Valid() {
		return false, errors.New("Invalid message.")
	}
	// 执行事务
	conn := p.Pool.Get()
	defer conn.Close()
	conn.Send("MULTI")
	conn.Send("HMSET", PREFIX_JOB_BUCKET+message.ID, "topic", message.Topic, "body", message.Body)
	conn.Send("EXPIRE", PREFIX_JOB_BUCKET+message.ID, delayTime+readyMaxLifetime)
	conn.Send("ZADD", KEY_JOB_POOL, time.Now().Unix()+int64(delayTime), message.ID)
	values, err := redis.Values(conn.Do("EXEC"))
	if err != nil {
		return false, err
	}
	// 事务结果处理
	v := values[0].(string)
	v1 := values[1].(int64)
	v2 := values[2].(int64)
	if v != "OK" || v1 == 0 || v2 == 0 {
		return false, nil
	}
	// 返回
	return true, nil
}

// 取出任务
func (p *Client) Pop(topic string) (*Message, error) {
	conn := p.Pool.Get()
	defer conn.Close()
	id, err := redis.String(conn.Do("RPOP", PREFIX_READY_QUEUE+topic))
	if err != nil {
		return nil, err
	}
	result, err := redis.StringMap(conn.Do("HGETALL", PREFIX_JOB_BUCKET+id))
	if err != nil {
		return nil, err
	}
	if result["topic"] == "" || result["body"] == "" {
		return nil, errors.New("Job bucket has expired or is incomplete")
	}
	conn.Do("DEL", PREFIX_JOB_BUCKET+id)
	msg := &Message{
		ID:    id,
		Topic: result["topic"],
		Body:  result["body"],
	}
	return msg, nil
}

// 阻塞取出任务
func (p *Client) BPop(topic string, timeout int) (*Message, error) {
	conn := p.Pool.Get()
	defer conn.Close()
	values, err := redis.Strings(conn.Do("BRPOP", PREFIX_READY_QUEUE+topic, timeout))
	if err != nil {
		return nil, err
	}
	id := values[1]
	result, err := redis.StringMap(conn.Do("HGETALL", PREFIX_JOB_BUCKET+id))
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	if result["topic"] == "" || result["body"] == "" {
		return nil, errors.New("Job bucket has expired or is incomplete")
	}
	conn.Do("DEL", PREFIX_JOB_BUCKET+id)
	msg := &Message{
		ID:    id,
		Topic: result["topic"],
		Body:  result["body"],
	}
	return msg, nil
}

// 移除任务
func (p *Client) Remove(id string) (bool, error) {
	// 执行事务
	conn := p.Pool.Get()
	defer conn.Close()
	conn.Send("MULTI")
	conn.Send("ZREM", KEY_JOB_POOL, id)
	conn.Send("DEL", PREFIX_JOB_BUCKET+id)
	values, err := redis.Values(conn.Do("EXEC"))
	if err != nil {
		return false, err
	}
	// 事务结果处理
	v := values[0].(int64)
	v1 := values[1].(int64)
	if v == 0 || v1 == 0 {
		return false, nil
	}
	// 返回
	return true, nil
}
