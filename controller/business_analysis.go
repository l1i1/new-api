/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const (
	defaultBusinessAnalysisDailyPeriods  = 14
	defaultBusinessAnalysisWeeklyPeriods = 8
	maxBusinessAnalysisDailyPeriods      = 60
	maxBusinessAnalysisWeeklyPeriods     = 52
)

func GetBusinessAnalysis(c *gin.Context) {
	dailyPeriods, ok := parseBusinessAnalysisPeriods(c.Query("daily_periods"), defaultBusinessAnalysisDailyPeriods, maxBusinessAnalysisDailyPeriods)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "daily_periods must be between 1 and 60"})
		return
	}
	weeklyPeriods, ok := parseBusinessAnalysisPeriods(c.Query("weekly_periods"), defaultBusinessAnalysisWeeklyPeriods, maxBusinessAnalysisWeeklyPeriods)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "weekly_periods must be between 1 and 52"})
		return
	}

	report, err := model.BuildBusinessAnalysisReport(dailyPeriods, weeklyPeriods, 0)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, report)
}

func parseBusinessAnalysisPeriods(raw string, fallback, maximum int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, false
	}
	return value, true
}
