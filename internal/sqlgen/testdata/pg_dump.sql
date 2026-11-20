--
-- PostgreSQL database dump
--

SET statement_timeout = 0;
SET client_encoding = 'UTF8';
SELECT pg_catalog.set_config('search_path', '', false);

CREATE TABLE public.products (
    id integer NOT NULL,
    title character varying(120) NOT NULL,
    price numeric(10,2) DEFAULT 0.00,
    tags text[],
    meta jsonb,
    added timestamp without time zone DEFAULT now() NOT NULL,
    note text DEFAULT 'n/a'::text,
    "SKU" uuid
);

ALTER TABLE public.products OWNER TO shop;

CREATE SEQUENCE public.products_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.products_id_seq OWNED BY public.products.id;

ALTER TABLE ONLY public.products ALTER COLUMN id SET DEFAULT nextval('public.products_id_seq'::regclass);

COPY public.products (id, title, price, tags, meta, added, note, "SKU") FROM stdin;
1	Tea	3.50	{green,hot}	{"a": 1}	2024-01-01 10:00:00	n/a	\N
2	Tab\there	0.00	\N	\N	2024-01-02 11:00:00	back\\slash	5f2b1c3e-1111-4a2b-9c3d-000000000001
\.

SELECT pg_catalog.setval('public.products_id_seq', 2, true);

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_pkey PRIMARY KEY (id);

COMMENT ON COLUMN public.products.title IS 'Shown in the shop';
