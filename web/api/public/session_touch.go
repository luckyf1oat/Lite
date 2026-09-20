package public

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/web/api"
)

func TouchSession(c *gin.Context) {
	if api.GetRole(c) != api.RoleAdmin {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	session, err := c.Cookie("session_token")
	if err != nil || session == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	expires, ttl, err := accounts.TouchSession(session, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		if errors.Is(err, accounts.ErrSessionExpired) {
			api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
			return
		}
		api.RespondError(c, http.StatusInternalServerError, "Failed to refresh session")
		return
	}
	setSessionCookie(c, session, ttl)
	now := time.Now().UTC()
	api.RespondSuccess(c, gin.H{
		"server_time": now,
		"expires_at":  expires,
		"ttl_seconds": ttl,
	})
}
