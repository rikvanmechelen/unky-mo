package review

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// routeLines parses src and lists its routes as "VERB path Controller#action
// helper", sorted, plus the skipped statements' texts.
func routeLines(t *testing.T, src string, files map[string]string) ([]string, []string) {
	t.Helper()
	read := func(p string) (string, bool) { s, ok := files[p]; return s, ok }
	rs := parseRailsRoutes(src, read, false)
	var out, skipped []string
	for _, r := range rs.Routes {
		out = append(out, strings.TrimSpace(r.Verb+" "+r.Path+" "+r.target()+" "+r.Helper))
	}
	for _, s := range rs.Skipped {
		skipped = append(skipped, s.Text)
	}
	sort.Strings(out)
	return out, skipped
}

func TestRailsRoutesParser(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		skipped   int
	}{
		{"resources", `Rails.application.routes.draw do
  resources :tickets
end`, []string{
			"DELETE /tickets/:id TicketsController#destroy",
			"GET /tickets TicketsController#index tickets",
			"GET /tickets/:id TicketsController#show ticket",
			"GET /tickets/:id/edit TicketsController#edit edit_ticket",
			"GET /tickets/new TicketsController#new new_ticket",
			"PATCH /tickets/:id TicketsController#update",
			"POST /tickets TicketsController#create",
			"PUT /tickets/:id TicketsController#update",
		}, 0},
		{"only, except, options", `resources :photos, only: %i[index show], controller: "images", path: "pics", as: "images", param: :slug
resources :news, only: [:index]
resources :users, except: [:destroy, :edit, :new, :update, :create]`, []string{
			"GET /news NewsController#index news_index",
			"GET /pics ImagesController#index images",
			"GET /pics/:slug ImagesController#show image",
			"GET /users UsersController#index users",
			"GET /users/:id UsersController#show user",
		}, 0},
		{"singular resource", `resource :profile, only: [:show, :edit, :update]`, []string{
			"GET /profile ProfilesController#show profile",
			"GET /profile/edit ProfilesController#edit edit_profile",
			"PATCH /profile ProfilesController#update",
			"PUT /profile ProfilesController#update",
		}, 0},
		{"nested, member, collection", `resources :tickets, only: [:show] do
  resources :comments, only: [:index, :show]
  member do
    post :refund
  end
  collection do
    get "search"
  end
  get :preview, on: :member
  get :export
end`, []string{
			"GET /tickets/:id TicketsController#show ticket",
			"GET /tickets/:id/preview TicketsController#preview preview_ticket",
			"GET /tickets/:ticket_id/comments CommentsController#index ticket_comments",
			"GET /tickets/:ticket_id/comments/:id CommentsController#show ticket_comment",
			"GET /tickets/:ticket_id/export TicketsController#export ticket_export",
			"GET /tickets/search TicketsController#search search_tickets",
			"POST /tickets/:id/refund TicketsController#refund refund_ticket",
		}, 0},
		{"shallow", `namespace :admin do
  resources :articles, only: [:show], shallow: true do
    resources :comments, only: [:index, :show]
  end
end
resources :posts, only: [] do
  resources :notes, only: [:index, :destroy], shallow: true
end
shallow do
  resources :boards, only: [] do
    resources :cards, only: [:show]
  end
end`, []string{
			"DELETE /notes/:id NotesController#destroy note",
			"GET /admin/articles/:article_id/comments Admin::CommentsController#index admin_article_comments",
			"GET /admin/articles/:id Admin::ArticlesController#show admin_article",
			"GET /admin/comments/:id Admin::CommentsController#show admin_comment",
			"GET /cards/:id CardsController#show card",
			"GET /posts/:post_id/notes NotesController#index post_notes",
		}, 0},
		{"namespace, scope, controller", `namespace :admin do
  get "stats", to: "dashboard#stats"
  scope module: "billing", as: "billing" do
    get "invoices/:id", to: "invoices#show", as: :invoice
  end
end
scope "(:locale)", locale: /en|fr/ do
  get "about", to: "pages#about"
end
scope path: "/api", module: :api do
  get "ping" => "health#ping"
end
controller :pages do
  get :terms
  get "privacy", action: :privacy_policy
end
namespace :v1, path: "", module: "version_one" do
  resources :items, only: :index
end`, []string{
			"GET /(:locale)/about PagesController#about about",
			"GET /admin/invoices/:id Admin::Billing::InvoicesController#show admin_billing_invoice",
			"GET /admin/stats Admin::DashboardController#stats admin_stats",
			"GET /api/ping Api::HealthController#ping ping",
			"GET /items VersionOne::ItemsController#index v1_items",
			"GET /privacy PagesController#privacy_policy privacy",
			"GET /terms PagesController#terms terms",
		}, 0},
		{"verbs and targets", `get "status" => "health#show", as: :health
post "webhooks/:source", controller: "webhooks", action: "receive"
match "search", to: "search#query", via: [:get, :post]
match "*path", to: "errors#not_found", via: :all
patch "photos/archive"
delete "/sessions", to: "/sessions#destroy"
get "legacy", to: "legacy#show", as: nil
put "up" => "health#show"`, []string{
			"ANY /*path ErrorsController#not_found",
			"DELETE /sessions SessionsController#destroy sessions",
			"GET /legacy LegacyController#show",
			"GET /search SearchController#query search",
			"GET /status HealthController#show health",
			"PATCH /photos/archive PhotosController#archive photos_archive",
			"POST /search SearchController#query search",
			"POST /webhooks/:source WebhooksController#receive",
			"PUT /up HealthController#show up",
		}, 0},
		{"root", `root "home#index"
namespace :admin do
  root to: "dashboard#show"
end
scope "/banana", as: "banana" do
  root :to => "home#index", :as => :banana_home
end`, []string{
			"GET / HomeController#index root",
			"GET /admin Admin::DashboardController#show admin_root",
			"GET /banana HomeController#index banana_banana_home",
		}, 0},
		{"concerns", `concern :commentable do
  resources :comments, only: [:index]
end
concern :archivable do
  post :archive, on: :member
end
resources :posts, only: [], concerns: :commentable
resources :videos, only: [], concerns: [:commentable, :archivable]
resources :songs, only: [] do
  concerns :archivable
end`, []string{
			"GET /posts/:post_id/comments CommentsController#index post_comments",
			"GET /videos/:video_id/comments CommentsController#index video_comments",
			"POST /songs/:id/archive SongsController#archive archive_song",
			"POST /videos/:id/archive VideosController#archive archive_video",
		}, 0},
		{"skipped", `require "sidekiq/web"
include SubdomainExtension
mount Sidekiq::Web => "/sidekiq"
get "/old", to: redirect("/new")
get "/lambda", to: ->(env) { [200, {}, ["ok"]] }
devise_for :users
ActiveAdmin.routes(self)
authenticate :user, ->(u) { u.admin? } do
  get "admin/tools", to: "tools#index"
end
subdomain "donate" do
  get "/give", to: "donations#new"
end
constraints(->(req) { req.format == :json }) do
  get "feed", to: "feeds#show"
end
if Rails.env.development?
  get "dev", to: "dev#index"
end
admin = lambda { |r| r.admin? }`, []string{
			"GET /admin/tools ToolsController#index admin_tools",
			"GET /dev DevController#index dev",
			"GET /feed FeedsController#show feed",
			"GET /give DonationsController#new give",
		}, 5},
		{"comments and strings", `# get "commented", to: "x#y"
=begin
resources :ghosts
=end
get "real", to: "pages#real" # resources :trailing
get "with#hash", to: "pages#hash"
get "x", to: "pages#x", constraints: { id: /\d+#[a-z]/ }
mailer_text = <<~TXT
  get "heredoc", to: "x#y"
TXT
get "after_heredoc", to: "pages#after"`, []string{
			"GET /after_heredoc PagesController#after after_heredoc",
			"GET /real PagesController#real real",
			"GET /with#hash PagesController#hash",
			"GET /x PagesController#x x",
		}, 0},
		{"loops over literal lists", `%w[about research].each do |section|
  get section, to: "pages#show", defaults: { slug: section }
  get "#{section}/*rest", to: "pages#show"
end`, []string{
			"GET /about PagesController#show about",
			"GET /about/*rest PagesController#show",
			"GET /research PagesController#show research",
			"GET /research/*rest PagesController#show",
		}, 0},
		{"multiline arguments", `post 'payment_callbacks/:id',
     to: 'lobby#receive', as: :callback
put '/donations', to: 'donations#validate', constraints: lambda { |request|
  request.request_parameters.has_key? 'validate'
}
resources :carts, only: %i[edit], param: :uuid do
  resources :donations, only: %i[edit]
end`, []string{
			"GET /carts/:cart_uuid/donations/:id/edit DonationsController#edit edit_cart_donation",
			"GET /carts/:uuid/edit CartsController#edit edit_cart",
			"POST /payment_callbacks/:id LobbyController#receive callback",
			"PUT /donations DonationsController#validate donations",
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, skipped := routeLines(t, c.src, nil)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("routes:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
			if len(skipped) != c.skipped {
				t.Errorf("skipped %d %q, want %d", len(skipped), skipped, c.skipped)
			}
		})
	}
}

func TestRailsRoutesDraw(t *testing.T) {
	files := map[string]string{
		"config/routes/admin.rb": "resources :users, only: [:index]\ndraw :loop\n",
		"config/routes/loop.rb":  "draw :admin\nget \"inner\", to: \"inner#show\"\n",
	}
	rs := parseRailsRoutes("namespace :admin do\n  draw(:admin)\nend\ndraw :missing\n", func(p string) (string, bool) { s, ok := files[p]; return s, ok }, false)
	var got []string
	for _, r := range rs.Routes {
		got = append(got, r.identity()+" "+r.target()+" "+r.File)
	}
	want := []string{
		"GET /admin/users Admin::UsersController#index config/routes/admin.rb",
		"GET /admin/inner Admin::InnerController#show config/routes/loop.rb",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("routes %v, want %v", got, want)
	}
	// The cycle (loop.rb drawing admin.rb again) and the missing file are skipped.
	if len(rs.Skipped) != 2 {
		t.Errorf("skipped %+v", rs.Skipped)
	}
}

func TestRailsRoutesAPIOnly(t *testing.T) {
	rs := parseRailsRoutes("resources :items\n", nil, true)
	for _, r := range rs.Routes {
		if r.Action == "new" || r.Action == "edit" {
			t.Errorf("api_only route %+v", r)
		}
	}
	if len(rs.Routes) != 6 {
		t.Errorf("%d routes", len(rs.Routes))
	}
}

// A routes file assembled from the forms a large app uses, as in an
// 882-line routes.rb: every line read, nothing skipped by accident.
func TestRailsRoutesLargeFile(t *testing.T) {
	var b strings.Builder
	b.WriteString("Rails.application.routes.draw do\n")
	for i := 0; i < 40; i++ {
		n := "res" + strings.Repeat("x", i%5) + string(rune('a'+i%26))
		b.WriteString("  namespace :ns" + n + " do\n")
		b.WriteString("    resources :" + n + "s, only: %i[index show] do\n      member do\n        post 'act'\n      end\n      collection do\n        get :search\n      end\n      resources :children, only: [:index]\n    end\n")
		b.WriteString("    get 'x/:id', to: '" + n + "s#x', as: :x\n")
		b.WriteString("    post '/y', to: 'y#create', constraints: lambda { |request|\n      request.params.key? 'y'\n    }\n")
		b.WriteString("  end\n")
	}
	b.WriteString("end\n")
	rs := parseRailsRoutes(b.String(), nil, false)
	if len(rs.Skipped) != 0 {
		t.Errorf("skipped %+v", rs.Skipped[:min(3, len(rs.Skipped))])
	}
	if len(rs.Routes) != 40*7 {
		t.Errorf("%d routes, want %d", len(rs.Routes), 40*7)
	}
}

func TestRouteChanges(t *testing.T) {
	before := parseRailsRoutes(`resources :tickets, only: [:index, :show]
get "about", to: "pages#about"
get "faq", to: "pages#faq"
mount Engine => "/engine"
`, nil, false)
	after := parseRailsRoutes(`get "faq", to: "help#faq"

# moved below, same route
get "about", to: "pages#about"
resources :tickets, only: [:index]
resources :tickets, only: [:create]
mount Other => "/other"
`, nil, false)
	var got []string
	for _, c := range routeChanges(before, after) {
		got = append(got, c.Op+" "+c.Name+" "+c.Detail)
	}
	sort.Strings(got)
	want := []string{
		"+ POST /tickets → TicketsController#create",
		"+ mount Other => \"/other\" route not read",
		"- GET /tickets/:id → TicketsController#show (ticket_path)",
		"- mount Engine => \"/engine\" route not read",
		"~ GET /faq → HelpController#faq (faq_path) (was PagesController#faq)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changes:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRailsRoutesSurface(t *testing.T) {
	dir := gitRepo(t)
	write(t, dir, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\n")
	write(t, dir, "config/application.rb", "module App\n  class Application < Rails::Application\n  end\nend\n")
	write(t, dir, "app/controllers/application_controller.rb", "class ApplicationController < ActionController::Base\nend\n")
	write(t, dir, "config/routes.rb", "Rails.application.routes.draw do\n  resources :tickets, only: [:index]\n  get \"about\", to: \"pages#about\"\nend\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "config/routes.rb", "Rails.application.routes.draw do\n  # about first now\n  get \"about\", to: \"pages#about\"\n  resources :tickets, only: [:index, :show]\nend\n")
	s := analyze(t, dir).Surface
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+GET /tickets/:id"}) {
		t.Errorf("routes %v", got)
	}
	if len(s.Routes) == 1 && (s.Routes[0].Detail != "→ TicketsController#show (ticket_path)" || s.Routes[0].Line != 4) {
		t.Errorf("route %+v", s.Routes[0])
	}
}
