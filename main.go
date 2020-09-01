package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/jinzhu/gorm"
	"github.com/qor/admin"
	"github.com/qor/publish2"
	"github.com/qor/qor"
	"github.com/qor/qor-example/app/account"
	adminapp "github.com/qor/qor-example/app/admin"
	"github.com/qor/qor-example/app/api"
	"github.com/qor/qor-example/app/enterprise"
	"github.com/qor/qor-example/app/home"
	"github.com/qor/qor-example/app/orders"
	"github.com/qor/qor-example/app/pages"
	"github.com/qor/qor-example/app/products"
	"github.com/qor/qor-example/app/static"
	"github.com/qor/qor-example/config"
	"github.com/qor/qor-example/config/application"
	"github.com/qor/qor-example/config/auth"
	"github.com/qor/qor-example/config/bindatafs"
	"github.com/qor/qor-example/config/db"
	_ "github.com/qor/qor-example/config/db/migrations"
	"github.com/qor/qor-example/utils/funcmapmaker"
	"github.com/qor/qor/resource"
	"github.com/qor/qor/utils"
	"github.com/qor/sorting"
)

func main() {
	cmdLine := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	compileTemplate := cmdLine.Bool("compile-templates", false, "Compile Templates")
	cmdLine.Parse(os.Args[1:])

	var (
		Router = chi.NewRouter()
		Admin  = admin.New(&admin.AdminConfig{
			SiteName: "QOR DEMO",
			Auth:     auth.AdminAuth{},
			DB:       db.DB.Set(publish2.VisibleMode, publish2.ModeOff).Set(publish2.ScheduleMode, publish2.ModeOff),
		})
		Application = application.New(&application.Config{
			Router: Router,
			Admin:  Admin,
			DB:     db.DB,
		})
	)

	InitDebugResource(Admin)
	funcmapmaker.AddFuncMapMaker(auth.Auth.Config.Render)

	Router.Use(func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// for demo, don't use this for your production site
			w.Header().Add("Access-Control-Allow-Origin", "*")
			handler.ServeHTTP(w, req)
		})
	})

	Router.Use(func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.Header.Del("Authorization")
			handler.ServeHTTP(w, req)
		})
	})

	Router.Use(middleware.RealIP)
	Router.Use(middleware.Logger)
	Router.Use(middleware.Recoverer)
	Router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var (
				tx         = db.DB
				qorContext = &qor.Context{Request: req, Writer: w}
			)

			if locale := utils.GetLocale(qorContext); locale != "" {
				tx = tx.Set("l10n:locale", locale)
			}

			ctx := context.WithValue(req.Context(), utils.ContextDBName, publish2.PreviewByDB(tx, qorContext))
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})

	Application.Use(api.New(&api.Config{}))
	Application.Use(adminapp.New(&adminapp.Config{}))
	Application.Use(home.New(&home.Config{}))
	Application.Use(products.New(&products.Config{}))
	Application.Use(account.New(&account.Config{}))
	Application.Use(orders.New(&orders.Config{}))
	Application.Use(pages.New(&pages.Config{}))
	Application.Use(enterprise.New(&enterprise.Config{}))
	Application.Use(static.New(&static.Config{
		Prefixs: []string{"/system"},
		Handler: utils.FileServer(http.Dir(filepath.Join(config.Root, "public"))),
	}))
	Application.Use(static.New(&static.Config{
		Prefixs: []string{"javascripts", "stylesheets", "images", "dist", "fonts", "vendors", "favicon.ico"},
		Handler: bindatafs.AssetFS.FileServer(http.Dir("public"), "javascripts", "stylesheets", "images", "dist", "fonts", "vendors", "favicon.ico"),
	}))

	if *compileTemplate {
		bindatafs.AssetFS.Compile()
	} else {
		fmt.Printf("Listening on: %v\n", config.Config.Port)
		if config.Config.HTTPS {
			if err := http.ListenAndServeTLS(fmt.Sprintf(":%d", config.Config.Port), "config/local_certs/server.crt", "config/local_certs/server.key", Application.NewServeMux()); err != nil {
				panic(err)
			}
		} else {
			if err := http.ListenAndServe(fmt.Sprintf(":%d", config.Config.Port), Application.NewServeMux()); err != nil {
				panic(err)
			}
		}
	}
}

func InitDebugResource(adm *admin.Admin) {
	db.DB.AutoMigrate(&Factory{}, &Item{})
	collection := adm.AddResource(&Factory{}, &admin.Config{Menu: []string{"Debug Management"}, Priority: -2})
	adm.AddResource(&Item{}, &admin.Config{Menu: []string{"Debug Management"}, Priority: -2})
	itemSelector := generateRemoteProductSelector(adm)
	collection.Meta(&admin.Meta{
		Name: "Items",
		Valuer: func(value interface{}, ctx *qor.Context) interface{} {
			coll := value.(*Factory)
			if err := ctx.GetDB().Set(publish2.VersionNameMode, "").Preload("Items").Find(coll).Error; err == nil {
				prods := []Item{}
				for _, p := range coll.Items {
					p.CompositePrimaryKey = fmt.Sprintf("%d%s%s", p.ID, resource.CompositePrimaryKeySeparator, p.GetVersionName())
					prods = append(prods, p)
				}
				fmt.Println("\n======================")
				fmt.Printf("%+v\n", prods)
				fmt.Println("======================")
				return prods
			}

			return ""
		},
		Config: &admin.SelectManyConfig{
			PrimaryField: "CompositePrimaryKey",
			Collection: func(value interface{}, ctx *qor.Context) (results [][]string) {
				if c, ok := value.(*Factory); ok {
					var items []Item
					ctx.GetDB().Model(c).Related(&items, "Items")

					for _, product := range items {
						results = append(results, []string{fmt.Sprintf("%v---%v", product.ID, product.GetVersionName()), product.Name})
					}
				}
				return
			},
			RemoteDataResource: itemSelector,
		},
	})
}

type Factory struct {
	gorm.Model
	Name string

	publish2.Version
	Items       []Item `gorm:"many2many:factory_items;association_autoupdate:false"`
	ItemsSorter sorting.SortableCollection
}

type Item struct {
	gorm.Model
	Name      string
	IDVersion string
	publish2.Version

	CompositePrimaryKey string `gorm:"-"`
}

func generateRemoteProductSelector(adm *admin.Admin) (res *admin.Resource) {
	res = adm.AddResource(&Item{}, &admin.Config{Name: "ItemSelector"})

	res.Meta(&admin.Meta{
		Name: "Name",
		Valuer: func(value interface{}, ctx *qor.Context) interface{} {
			if r, ok := value.(*Item); ok {
				return r.Name
			}
			return ""
		},
	})
	res.IndexAttrs("ID", "Name", "CompositePrimaryKey")
	res.SearchAttrs("Name")

	res.Meta(&admin.Meta{
		Name: "CompositePrimaryKey",
		Valuer: func(value interface{}, ctx *qor.Context) interface{} {
			if r, ok := value.(*Item); ok {
				return fmt.Sprintf("%d%s%s", r.ID, resource.CompositePrimaryKeySeparator, r.GetVersionName())
			}
			return ""
		},
	})

	res.Scope(&admin.Scope{
		Name:    "",
		Default: true,
		Handler: func(db *gorm.DB, ctx *qor.Context) *gorm.DB {
			return db
		},
	})

	return res
}
