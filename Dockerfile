FROM golang:1.24 AS builder

# Устанавливаем рабочий каталог внутри контейнера
WORKDIR /src/

# Копируем исходники в контейнер
COPY . /src/

# Скачиваем все зависимости
RUN go get ./...

# Собираем бинарный файл
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o app .

# Используем образ alpine:latest как базовый
FROM alpine:latest  

# Устанавливаем рабочую директорию
WORKDIR /

COPY ./migrations /migrations

# Копируем бинарный файл из этапа builder
COPY --from=builder /src/app .
EXPOSE 8080
# Запускаем приложение 
ENTRYPOINT ["./app"]