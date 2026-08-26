package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PromoCodeUsage holds the schema definition for the PromoCodeUsage entity.
//
// 优惠码使用记录：记录每个用户使用优惠码的情况
type PromoCodeUsage struct {
	ent.Schema
}

func (PromoCodeUsage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "promo_code_usages"},
	}
}

func (PromoCodeUsage) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("promo_code_id").
			Comment("优惠码ID"),
		field.Int64("user_id").
			Comment("使用用户ID"),
		field.Int64("payment_order_id").
			Optional().
			Nillable().
			Comment("订阅支付订单ID"),
		field.String("usage_type").
			MaxLen(32).
			Default("registration_bonus").
			Comment("用途: registration_bonus, subscription_discount"),
		field.String("status").
			MaxLen(20).
			Default("consumed").
			Comment("状态: reserved, consumed, released"),
		field.Float("bonus_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Comment("实际赠送金额"),
		field.Float("discount_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Default(0).
			Comment("订阅订单优惠金额"),
		field.Time("used_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("使用时间"),
		field.Time("reserved_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("consumed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("released_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PromoCodeUsage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("promo_code", PromoCode.Type).
			Ref("usage_records").
			Field("promo_code_id").
			Required().
			Unique(),
		edge.From("user", User.Type).
			Ref("promo_code_usages").
			Field("user_id").
			Required().
			Unique(),
	}
}

func (PromoCodeUsage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("promo_code_id"),
		index.Fields("user_id"),
		index.Fields("status").StorageKey("idx_promo_code_usages_status"),
		index.Fields("promo_code_id", "user_id").
			Unique().
			StorageKey("uq_promo_registration_user").
			Annotations(entsql.IndexWhere("usage_type = 'registration_bonus'")),
		index.Fields("promo_code_id").
			Unique().
			StorageKey("uq_promo_subscription_reserved").
			Annotations(entsql.IndexWhere("usage_type = 'subscription_discount' AND status = 'reserved'")),
		index.Fields("promo_code_id").
			Unique().
			StorageKey("uq_promo_subscription_consumed").
			Annotations(entsql.IndexWhere("usage_type = 'subscription_discount' AND status = 'consumed'")),
		index.Fields("payment_order_id").
			Unique().
			StorageKey("uq_promo_usage_payment_order").
			Annotations(entsql.IndexWhere("payment_order_id IS NOT NULL")),
	}
}
