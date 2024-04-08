package domainmetrics

import "github.com/gin-gonic/gin"

func NewDomainMetricsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "domain metrics",
		})
	}
}
