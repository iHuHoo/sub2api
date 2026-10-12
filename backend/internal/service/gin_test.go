package service

import "github.com/gin-gonic/gin"

// Set the global mode before any parallel tests or background workers start.
func init() { gin.SetMode(gin.TestMode) }
