package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Logger *zap.Logger

var (
	Any    = zap.Any
	String = zap.String
	Uint   = zap.Uint
)

func InitLogger(logpath, loglevel string) {
	hook := lumberjack.Logger{
		Filename:   logpath,
		MaxSize:    100,
		MaxBackups: 30,
		MaxAge:     7,
		Compress:   true,
	}

	write := zapcore.AddSync(&hook)

	var level zapcore.Level
	switch loglevel {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "error":
		level = zap.ErrorLevel
	case "warn":
		level = zap.WarnLevel
	default:
		level = zap.InfoLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "linenum",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.FullCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	writes := []zapcore.WriteSyncer{write}
	if level == zap.DebugLevel {
		writes = append(writes, zapcore.AddSync(os.Stdout))
	}

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.NewMultiWriteSyncer(writes...),
		level,
	)

	Logger = zap.New(
		core,
		zap.AddCaller(),
		zap.Development(),
		zap.Fields(zap.String("app", "chat")),
	)
}

func Debug(msg string, fields ...zap.Field) {
	if Logger == nil {
		return
	}
	Logger.Debug(msg, fields...)
}

func Info(msg string, fields ...zap.Field) {
	if Logger == nil {
		return
	}
	Logger.Info(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	if Logger == nil {
		return
	}
	Logger.Warn(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	if Logger == nil {
		return
	}
	Logger.Error(msg, fields...)
}
