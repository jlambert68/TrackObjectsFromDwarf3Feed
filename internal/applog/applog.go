package applog

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
)

var (
	infoLogger  = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	errorLogger = log.New(os.Stderr, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func InfofID(id, format string, args ...any) {
	output(infoLogger, "INFO", id, format, args...)
}

func ErrorfID(id, format string, args ...any) {
	output(errorLogger, "ERROR", id, format, args...)
}

func DebugfID(id, format string, args ...any) {
	output(infoLogger, "DEBUG", id, format, args...)
}

func output(logger *log.Logger, level, id, format string, args ...any) {
	if !uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(id))) {
		panic(fmt.Sprintf("invalid log UUID: %q", id))
	}
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	_ = logger.Output(3, fmt.Sprintf("level=%s ID=%s %s", level, id, message))
}
