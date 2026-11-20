USE [Shop]
GO
/****** Object:  Table [dbo].[Employees]    Script Date: 01.02.2024 10:00:00 ******/
SET ANSI_NULLS ON
GO
SET QUOTED_IDENTIFIER ON
GO
CREATE TABLE [dbo].[Employees](
	[EmployeeID] [int] IDENTITY(1,1) NOT NULL,
	[FullName] [nvarchar](100) NOT NULL,
	[Notes] [nvarchar](max) NULL,
	[Salary] [money] NULL,
	[Active] [bit] NOT NULL,
	[HiredOn] [datetime] NULL,
 CONSTRAINT [PK_Employees] PRIMARY KEY CLUSTERED 
(
	[EmployeeID] ASC
)WITH (PAD_INDEX = OFF, STATISTICS_NORECOMPUTE = OFF) ON [PRIMARY]
) ON [PRIMARY] TEXTIMAGE_ON [PRIMARY]
GO
SET IDENTITY_INSERT [dbo].[Employees] ON 
GO
INSERT [dbo].[Employees] ([EmployeeID], [FullName], [Notes], [Salary], [Active], [HiredOn]) VALUES (1, N'Иван Петров', NULL, 1500.0000, 1, CAST(N'2020-05-01T00:00:00.000' AS DateTime))
GO
INSERT [dbo].[Employees] ([EmployeeID], [FullName], [Notes], [Salary], [Active], [HiredOn]) VALUES (2, N'Ann [x]', N'it''s', NULL, 0, NULL)
GO
SET IDENTITY_INSERT [dbo].[Employees] OFF
GO
ALTER TABLE [dbo].[Employees] ADD  CONSTRAINT [DF_Employees_Active]  DEFAULT ((1)) FOR [Active]
GO
