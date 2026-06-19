package middleware

import (
	"gossh/app/config"
	"gossh/app/model"
	"gossh/gin"
	"sync/atomic"
	"time"
)

var lastDbCheckOk atomic.Bool
var lastDbCheckTime int64

func DbCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.DefaultConfig.IsInit {
			c.Next()
			return
		}
		if model.Db != nil {
			// 5 秒内不重复检查
			now := time.Now().Unix()
			if lastDbCheckOk.Load() && now-lastDbCheckTime < 5 {
				c.Next()
				return
			}
			tx := model.Db.Exec("select 1=1")
			if tx.Error == nil {
				lastDbCheckOk.Store(true)
				lastDbCheckTime = now
				c.Next()
			} else {
				lastDbCheckOk.Store(false)
				err := model.DbMigrate(config.DefaultConfig.DbType, config.DefaultConfig.DbFile)
				if err != nil {
					c.Abort()
					c.JSON(500, gin.H{"code": 500, "msg": "数据库连接错误:" + err.Error()})
					return
				}
			}
		}
		c.Next()
	}
}
