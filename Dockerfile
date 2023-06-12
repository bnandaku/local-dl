FROM golang:latest AS build
WORKDIR /go/src
COPY . .
RUN apt-get install ca-certificates -y
RUN go get
RUN go build -a -installsuffix cgo -o app .

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /go/src/app ./
COPY media /
#COPY .env ./
#COPY pamela ./
ENTRYPOINT ["./app"]
