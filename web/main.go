package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/geekbrains-gomicrosevices/lesson4/pkg/grpc/user"
	"github.com/geekbrains-gomicrosevices/lesson4/pkg/jwt"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
)

type Config struct {
	Addr         string
	UserGRPCAddr string
	UserAddr     string
	MovieAddr    string
}

var cfg = Config{
	Addr:         ":8082",
	UserGRPCAddr: ":1234",
	UserAddr:     "http://localhost:8081",
	MovieAddr:    "http://localhost:8080",
}

var TT struct {
	MovieList *template.Template
	Login     *template.Template
}

var UserCli user.UserClient

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/", MainHandler)

	r.HandleFunc("/login", LoginFormHandler).Methods("Get")
	r.HandleFunc("/login", LoginHandler).Methods("POST")
	r.HandleFunc("/logout", LogoutHandler).Methods("POST")

	conn, err := grpc.Dial(cfg.UserGRPCAddr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("did not connect: %s", err)
	}
	UserCli = user.NewUserClient(conn)

	fs := http.FileServer(http.Dir("assets"))
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", fs))

	TT.MovieList, err = template.ParseFiles("template/layout/base.html", "template/main.html")
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Name: %s", TT.MovieList.Name())

	TT.Login, err = template.ParseFiles("template/layout/base.html", "template/login.html")
	if err != nil {
		log.Fatal(err)
	}

	http.ListenAndServe(cfg.Addr, r)
}

type MainPage struct {
	Movies *[]Movie
	User   User
}

type User struct {
	Name   string
	IsPaid bool
}

type Movie struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Poster   string `json:"poster"`
	MovieUrl string `json:"movie_url"`
	IsPaid   bool   `json:"is_paid"`
}

func MainHandler(w http.ResponseWriter, r *http.Request) {

	page := MainPage{}

	var err error
	page.Movies, err = getMovies()
	if err != nil {
		log.Printf("Get movie error: %v", err)
	}

	page.User, err = getUserByToken(r)
	if err != nil {
		log.Printf("Get user error: %v", err)
	}

	log.Printf("User: %+v", page.User)

	err = TT.MovieList.ExecuteTemplate(w, "base", page)
	if err != nil {
		log.Printf("Render error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

type LoginPage struct {
	User  User
	Error string
}

func LoginFormHandler(w http.ResponseWriter, r *http.Request) {
	page := &LoginPage{}

	var err error
	page.User, err = getUserByToken(r)
	if err != nil {
		log.Printf("No user: %v", err)
		// В случае не валидного токена показываем страницу логина
		TT.Login.ExecuteTemplate(w, "base", page)
		return
	}

	TT.Login.ExecuteTemplate(w, "base", page)
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	page := &LoginPage{}

	r.ParseForm()
	email := r.PostFormValue("email")
	pwd := r.PostFormValue("pwd")

	res, err := UserCli.Login(
		context.Background(),
		&user.LoginRequest{Email: email, Pwd: pwd},
	)

	// Что-то не так с сервисом user
	if err != nil {
		log.Printf("Get user error: %v", err)
		page.Error = "Сервис авторизации не доступен"
		TT.Login.ExecuteTemplate(w, "base", page)
		return
	}

	// Ошибка логина, ее можно показать пользователю
	if res.GetError() != "" {
		page.Error = res.GetError()
		TT.Login.ExecuteTemplate(w, "base", page)
		return
	}

	tok := res.GetJwt()

	// Если пользователь успешно залогинен записываем токен в cookie
	http.SetCookie(w, &http.Cookie{Name: "jwt", Value: tok})

	jwtData, err := jwt.Parse(tok)
	if err != nil {
		// В случае не валидного токена показываем страницу логина
		TT.Login.ExecuteTemplate(w, "base", page)
		return
	}

	page.User = User{Name: jwtData.Name}
	TT.Login.ExecuteTemplate(w, "base", page)
}

func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "jwt", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func getMovies() (*[]Movie, error) {
	mm := &[]Movie{}
	err := get(cfg.MovieAddr+"/movie", mm)
	if err != nil {
		return nil, err
	}

	return mm, nil
}

var ERR_NO_JWT = errors.New("No 'jwt' cookie")

func getUserByToken(r *http.Request) (u User, err error) {
	tok, err := r.Cookie("jwt")
	if tok == nil {
		return u, ERR_NO_JWT
	}

	jwtData, err := jwt.Parse(tok.Value)
	if err != nil {
		return u, fmt.Errorf("Can't parse toke: %w", err)
	}

	u.Name = jwtData.Name
	u.IsPaid = jwtData.IsPaid
	return u, err
}

func getUser(r *http.Request) (u User, err error) {
	ses, err := r.Cookie("session")
	if ses == nil {
		return u, err
	}

	res := &struct {
		User
		Error string
	}{}
	err = get(cfg.UserAddr+"/user?token="+ses.Value, res)
	if err != nil {
		return u, err
	}

	if res.Error != "" {
		return u, fmt.Errorf(res.Error)
	}

	return User{
		Name:   res.Name,
		IsPaid: true,
	}, err
}

func post(url string, in url.Values, out interface{}) error {
	r, err := http.DefaultClient.PostForm(url, in)
	if err != nil {
		return fmt.Errorf("make POST request error: %w", err)
	}

	return parseResponse(r, out)
}

func get(url string, out interface{}) error {
	r, err := http.DefaultClient.Get(url)
	if err != nil {
		return fmt.Errorf("make GET request error: %w", err)
	}

	return parseResponse(r, out)
}

func parseResponse(res *http.Response, out interface{}) error {
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read response error: %w", err)
	}

	err = json.Unmarshal(body, out)
	fmt.Printf("%s", body)
	if err != nil {
		return fmt.Errorf("parse body error '%s': %w", body, err)
	}

	return nil
}
