package core

// Request bodies for the library sync RPC, converted from the
// field maps in gpmc (github.com/xob0t/google_photos_mobile_client, MIT
// license), gpmc/api.py. Most of each body is a field mask naming what the
// response should include; only the tokens vary.

// libraryStateRequest starts a library sync; an empty syncToken asks for the
// whole library, paged with resume tokens.
func libraryStateRequest(syncToken string) pbm {
	return pbm{
		{1, pbm{
			{1, pbm{
				{1, pbm{
					{1, pbm{}},
					{3, pbm{}},
					{4, pbm{}},
					{5, pbm{
						{1, pbm{}},
						{2, pbm{}},
						{3, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{7, pbm{}},
					}},
					{6, pbm{}},
					{7, pbm{
						{2, pbm{}},
					}},
					{15, pbm{}},
					{16, pbm{}},
					{17, pbm{}},
					{19, pbm{}},
					{20, pbm{}},
					{21, pbm{
						{5, pbm{
							{3, pbm{}},
						}},
						{6, pbm{}},
					}},
					{25, pbm{}},
					{30, pbm{
						{2, pbm{}},
					}},
					{31, pbm{}},
					{32, pbm{}},
					{33, pbm{
						{1, pbm{}},
					}},
					{34, pbm{}},
					{36, pbm{}},
					{37, pbm{}},
					{38, pbm{}},
					{39, pbm{}},
					{40, pbm{}},
					{41, pbm{}},
				}},
				{5, pbm{
					{2, pbm{
						{2, pbm{
							{3, pbm{
								{2, pbm{}},
							}},
							{4, pbm{
								{2, pbm{}},
								{4, pbm{}},
							}},
						}},
						{4, pbm{
							{2, pbm{
								{2, 1},
							}},
						}},
						{5, pbm{
							{2, pbm{}},
						}},
						{6, 1},
					}},
					{3, pbm{
						{2, pbm{
							{3, pbm{}},
							{4, pbm{}},
						}},
						{3, pbm{
							{2, pbm{}},
							{3, pbm{
								{2, 1},
								{3, pbm{}},
							}},
						}},
						{4, pbm{}},
						{5, pbm{
							{2, pbm{
								{2, 1},
							}},
						}},
						{7, pbm{}},
					}},
					{4, pbm{
						{2, pbm{
							{2, pbm{}},
						}},
					}},
					{5, pbm{
						{1, pbm{
							{2, pbm{
								{3, pbm{}},
								{4, pbm{}},
							}},
							{3, pbm{
								{2, pbm{}},
								{3, pbm{
									{2, 1},
									{3, pbm{}},
								}},
							}},
						}},
						{3, 1},
					}},
				}},
				{8, pbm{}},
				{9, pbm{
					{2, pbm{}},
					{3, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
					{4, pbm{
						{1, pbm{
							{3, pbm{
								{1, pbm{
									{1, pbm{
										{5, pbm{
											{1, pbm{}},
										}},
										{6, pbm{}},
										{7, pbm{}},
									}},
									{2, pbm{}},
									{3, pbm{
										{1, pbm{
											{5, pbm{
												{1, pbm{}},
											}},
											{6, pbm{}},
											{7, pbm{}},
										}},
										{2, pbm{}},
									}},
								}},
							}},
							{4, pbm{
								{1, pbm{
									{2, pbm{}},
								}},
							}},
						}},
					}},
				}},
				{11, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{12, pbm{}},
				{14, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{15, pbm{
					{1, pbm{}},
					{4, pbm{}},
				}},
				{17, pbm{
					{1, pbm{}},
					{4, pbm{}},
				}},
				{19, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{21, pbm{
					{1, pbm{}},
				}},
				{22, pbm{}},
				{23, pbm{}},
				{24, pbm{}},
			}},
			{2, pbm{
				{1, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{}},
					{5, pbm{}},
					{6, pbm{
						{1, pbm{}},
						{2, pbm{}},
						{3, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{7, pbm{}},
					}},
					{7, pbm{}},
					{8, pbm{}},
					{10, pbm{}},
					{12, pbm{}},
					{13, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{15, pbm{
						{1, pbm{}},
					}},
					{18, pbm{}},
				}},
				{4, pbm{
					{1, pbm{}},
				}},
				{9, pbm{}},
				{11, pbm{
					{1, pbm{
						{1, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{6, pbm{}},
						{9, pbm{}},
					}},
				}},
				{14, pbm{
					{1, pbm{
						{1, pbm{
							{1, pbm{}},
							{2, pbm{
								{2, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
							}},
							{3, pbm{
								{4, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
								{5, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
							}},
						}},
						{2, pbm{}},
					}},
				}},
				{17, pbm{}},
				{18, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
					}},
				}},
				{20, pbm{
					{2, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
				}},
				{22, pbm{}},
				{23, pbm{}},
				{24, pbm{}},
			}},
			{3, pbm{
				{2, pbm{}},
				{3, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{7, pbm{}},
					{8, pbm{}},
					{14, pbm{
						{1, pbm{}},
					}},
					{16, pbm{}},
					{17, pbm{
						{2, pbm{}},
					}},
					{18, pbm{}},
					{19, pbm{}},
					{20, pbm{}},
					{21, pbm{}},
					{22, pbm{}},
					{23, pbm{}},
					{27, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
					}},
					{29, pbm{}},
					{30, pbm{}},
					{31, pbm{}},
					{32, pbm{}},
					{34, pbm{}},
					{37, pbm{}},
					{38, pbm{}},
					{39, pbm{}},
					{41, pbm{}},
					{43, pbm{
						{1, pbm{}},
					}},
					{45, pbm{
						{1, pbm{
							{1, pbm{}},
						}},
					}},
					{46, pbm{
						{1, pbm{}},
						{2, pbm{}},
						{3, pbm{}},
					}},
					{47, pbm{}},
				}},
				{4, pbm{
					{2, pbm{}},
					{3, pbm{
						{1, pbm{}},
					}},
					{4, pbm{}},
					{5, pbm{
						{1, pbm{}},
					}},
				}},
				{7, pbm{}},
				{12, pbm{}},
				{13, pbm{}},
				{14, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
						{3, pbm{}},
						{4, pbm{
							{1, pbm{}},
						}},
					}},
					{3, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
						{3, pbm{}},
						{4, pbm{}},
					}},
				}},
				{15, pbm{}},
				{16, pbm{
					{1, pbm{}},
				}},
				{18, pbm{}},
				{19, pbm{
					{4, pbm{
						{2, pbm{}},
					}},
					{6, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{7, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{8, pbm{}},
					{9, pbm{}},
				}},
				{20, pbm{}},
				{22, pbm{}},
				{24, pbm{}},
				{25, pbm{}},
				{26, pbm{}},
			}},
			{6, syncToken},
			{7, 2},
			{9, pbm{
				{1, pbm{
					{2, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
				}},
				{2, pbm{
					{3, pbm{
						{2, 1},
					}},
				}},
				{3, pbm{
					{2, pbm{}},
				}},
				{4, pbm{}},
				{7, pbm{
					{1, pbm{}},
				}},
				{8, pbm{
					{1, 2},
					{2, "\x01\x02\x03\x05\x06\x07"},
				}},
				{9, pbm{}},
				{11, pbm{
					{1, pbm{}},
				}},
			}},
			{11, []int{1, 2, 6}},
			{12, pbm{
				{2, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{3, pbm{
					{1, pbm{}},
				}},
				{4, pbm{}},
			}},
			{13, pbm{}},
			{15, pbm{
				{3, pbm{
					{1, 1},
				}},
			}},
			{18, pbm{
				{169945741, pbm{
					{1, pbm{
						{1, pbm{
							{4, []int{2, 1, 6, 8, 10, 15, 18, 13, 17, 19, 14, 20}},
							{5, 6},
							{6, 2},
							{7, 1},
							{8, 2},
							{11, 3},
							{12, 1},
							{13, 3},
							{15, 1},
							{16, 1},
							{17, 1},
							{18, 2},
						}},
					}},
				}},
			}},
			{19, pbm{
				{1, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{2, pbm{
					{1, []int{1, 2, 4, 6, 5, 7}},
				}},
				{3, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{5, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{6, pbm{
					{1, pbm{}},
				}},
				{7, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{8, pbm{
					{1, pbm{}},
				}},
			}},
			{20, pbm{
				{1, 1},
				{2, ""},
				{3, pbm{
					{1, "type.googleapis.com/photos.printing.client.PrintingPromotionSyncOptions"},
					{2, pbm{
						{1, pbm{
							{4, []int{2, 1, 6, 8, 10, 15, 18, 13, 17, 19, 14, 20}},
							{5, 6},
							{6, 2},
							{7, 1},
							{8, 2},
							{11, 3},
							{12, 1},
							{13, 3},
							{15, 1},
							{16, 1},
							{17, 1},
							{18, 2},
						}},
					}},
				}},
			}},
			{21, pbm{
				{2, pbm{
					{2, pbm{
						{4, pbm{}},
					}},
					{4, pbm{}},
					{5, pbm{}},
				}},
				{3, pbm{
					{2, pbm{
						{1, 1},
					}},
					{4, pbm{
						{2, pbm{}},
					}},
				}},
				{5, pbm{
					{1, pbm{}},
				}},
				{6, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
					}},
				}},
				{7, pbm{
					{1, 2},
					{2, "\x01\x07\x08\x09\x0a\x0d\x0e\x0f\x11\x13\x14\x16\x17-./01:\x06\x18267;>?@A89<GBED"},
					{3, "\x01"},
				}},
				{8, pbm{
					{3, pbm{
						{1, pbm{
							{1, pbm{
								{2, pbm{
									{1, 1},
								}},
								{4, pbm{
									{2, pbm{}},
								}},
							}},
						}},
						{3, pbm{}},
					}},
					{4, pbm{
						{1, pbm{}},
					}},
					{5, pbm{
						{1, pbm{
							{2, pbm{
								{1, 1},
							}},
							{4, pbm{
								{2, pbm{}},
							}},
						}},
					}},
				}},
				{9, pbm{
					{1, pbm{}},
				}},
				{10, pbm{
					{1, pbm{
						{1, pbm{}},
					}},
					{3, pbm{}},
					{5, pbm{}},
					{6, pbm{
						{1, pbm{}},
					}},
					{7, pbm{}},
					{9, pbm{}},
					{10, pbm{}},
				}},
				{11, pbm{}},
				{12, pbm{}},
				{13, pbm{}},
				{14, pbm{}},
				{16, pbm{
					{1, pbm{}},
				}},
			}},
			{22, pbm{
				{1, 1},
				{2, "107818234414673686888"},
			}},
			{25, pbm{
				{1, pbm{
					{1, pbm{
						{1, pbm{
							{1, pbm{}},
						}},
					}},
				}},
				{2, pbm{}},
			}},
			{26, pbm{}},
		}},
		{2, pbm{
			{1, pbm{
				{1, pbm{
					{1, pbm{
						{1, pbm{}},
					}},
					{2, pbm{}},
				}},
			}},
			{2, pbm{}},
		}},
	}
}

// libraryPageInitRequest fetches the next page of an initial sync.
func libraryPageInitRequest(resumeToken string) pbm {
	return pbm{
		{1, pbm{
			{1, pbm{
				{1, pbm{
					{1, pbm{}},
					{3, pbm{}},
					{4, pbm{}},
					{5, pbm{
						{1, pbm{}},
						{2, pbm{}},
						{3, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{7, pbm{}},
					}},
					{6, pbm{}},
					{7, pbm{
						{2, pbm{}},
					}},
					{15, pbm{}},
					{16, pbm{}},
					{17, pbm{}},
					{19, pbm{}},
					{20, pbm{}},
					{21, pbm{
						{5, pbm{
							{3, pbm{}},
						}},
						{6, pbm{}},
					}},
					{25, pbm{}},
					{30, pbm{
						{2, pbm{}},
					}},
					{31, pbm{}},
					{32, pbm{}},
					{33, pbm{
						{1, pbm{}},
					}},
					{34, pbm{}},
					{36, pbm{}},
					{37, pbm{}},
					{38, pbm{}},
					{39, pbm{}},
					{40, pbm{}},
					{41, pbm{}},
				}},
				{5, pbm{
					{2, pbm{
						{2, pbm{
							{3, pbm{
								{2, pbm{}},
							}},
							{4, pbm{
								{2, pbm{}},
							}},
						}},
						{4, pbm{
							{2, pbm{
								{2, 1},
							}},
						}},
						{5, pbm{
							{2, pbm{}},
						}},
						{6, 1},
					}},
					{3, pbm{
						{2, pbm{
							{3, pbm{}},
							{4, pbm{}},
						}},
						{3, pbm{
							{2, pbm{}},
							{3, pbm{
								{2, 1},
							}},
						}},
						{4, pbm{}},
						{5, pbm{
							{2, pbm{
								{2, 1},
							}},
						}},
						{7, pbm{}},
					}},
					{4, pbm{
						{2, pbm{
							{2, pbm{}},
						}},
					}},
					{5, pbm{
						{1, pbm{
							{2, pbm{
								{3, pbm{}},
								{4, pbm{}},
							}},
							{3, pbm{
								{2, pbm{}},
								{3, pbm{
									{2, 1},
								}},
							}},
						}},
						{3, 1},
					}},
				}},
				{8, pbm{}},
				{9, pbm{
					{2, pbm{}},
					{3, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
					{4, pbm{
						{1, pbm{
							{3, pbm{
								{1, pbm{
									{1, pbm{
										{5, pbm{
											{1, pbm{}},
										}},
										{6, pbm{}},
									}},
									{2, pbm{}},
									{3, pbm{
										{1, pbm{
											{5, pbm{
												{1, pbm{}},
											}},
											{6, pbm{}},
										}},
										{2, pbm{}},
									}},
								}},
							}},
							{4, pbm{
								{1, pbm{
									{2, pbm{}},
								}},
							}},
						}},
					}},
				}},
				{11, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{12, pbm{}},
				{14, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{15, pbm{
					{1, pbm{}},
					{4, pbm{}},
				}},
				{17, pbm{
					{1, pbm{}},
					{4, pbm{}},
				}},
				{19, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{
						{2, pbm{
							{1, 1},
							{2, 2},
						}},
					}},
				}},
				{22, pbm{}},
				{23, pbm{}},
			}},
			{2, pbm{
				{1, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{}},
					{5, pbm{}},
					{6, pbm{
						{1, pbm{}},
						{2, pbm{}},
						{3, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{7, pbm{}},
					}},
					{7, pbm{}},
					{8, pbm{}},
					{10, pbm{}},
					{12, pbm{}},
					{13, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{15, pbm{
						{1, pbm{}},
					}},
					{18, pbm{}},
				}},
				{4, pbm{
					{1, pbm{}},
				}},
				{9, pbm{}},
				{11, pbm{
					{1, pbm{
						{1, pbm{}},
						{4, pbm{}},
						{5, pbm{}},
						{6, pbm{}},
						{9, pbm{}},
					}},
				}},
				{14, pbm{
					{1, pbm{
						{1, pbm{
							{1, pbm{}},
							{2, pbm{
								{2, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
							}},
							{3, pbm{
								{4, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
								{5, pbm{
									{1, pbm{
										{1, pbm{}},
									}},
									{3, pbm{}},
								}},
							}},
						}},
						{2, pbm{}},
					}},
				}},
				{17, pbm{}},
				{18, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
					}},
				}},
				{20, pbm{
					{2, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
				}},
				{23, pbm{}},
			}},
			{3, pbm{
				{2, pbm{}},
				{3, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{7, pbm{}},
					{8, pbm{}},
					{14, pbm{
						{1, pbm{}},
					}},
					{16, pbm{}},
					{17, pbm{
						{2, pbm{}},
					}},
					{18, pbm{}},
					{19, pbm{}},
					{20, pbm{}},
					{21, pbm{}},
					{22, pbm{}},
					{23, pbm{}},
					{27, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
					}},
					{29, pbm{}},
					{30, pbm{}},
					{31, pbm{}},
					{32, pbm{}},
					{34, pbm{}},
					{37, pbm{}},
					{38, pbm{}},
					{39, pbm{}},
					{41, pbm{}},
				}},
				{4, pbm{
					{2, pbm{}},
					{3, pbm{}},
					{4, pbm{}},
				}},
				{7, pbm{}},
				{12, pbm{}},
				{13, pbm{}},
				{14, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
						{3, pbm{}},
						{4, pbm{
							{1, pbm{}},
						}},
					}},
					{3, pbm{
						{1, pbm{}},
						{2, pbm{
							{1, pbm{}},
						}},
						{3, pbm{}},
						{4, pbm{}},
					}},
				}},
				{15, pbm{}},
				{16, pbm{
					{1, pbm{}},
				}},
				{18, pbm{}},
				{19, pbm{
					{4, pbm{
						{2, pbm{}},
					}},
					{6, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{7, pbm{
						{2, pbm{}},
						{3, pbm{}},
					}},
					{8, pbm{}},
				}},
				{20, pbm{}},
				{24, pbm{}},
				{25, pbm{}},
			}},
			{4, resumeToken},
			{7, 2},
			{9, pbm{
				{1, pbm{
					{2, pbm{
						{1, pbm{}},
						{2, pbm{}},
					}},
				}},
				{2, pbm{
					{3, pbm{
						{2, 1},
					}},
				}},
				{3, pbm{
					{2, pbm{}},
				}},
				{4, pbm{}},
				{7, pbm{
					{1, pbm{}},
				}},
				{8, pbm{
					{1, 2},
					{2, "\x01\x02\x03\x05\x06"},
				}},
				{9, pbm{}},
			}},
			{11, []int{1, 2}},
			{12, pbm{
				{2, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{3, pbm{
					{1, pbm{}},
				}},
				{4, pbm{}},
			}},
			{13, pbm{}},
			{15, pbm{
				{3, pbm{
					{1, 1},
				}},
			}},
			{18, pbm{
				{169945741, pbm{
					{1, pbm{
						{1, pbm{
							{4, []int{2, 1, 6, 8, 10, 15, 18, 13, 17, 19, 14, 20}},
							{5, 6},
							{6, 2},
							{7, 1},
							{8, 2},
							{11, 3},
							{12, 1},
							{13, 3},
							{15, 1},
							{16, 1},
							{17, 1},
							{18, 2},
						}},
					}},
				}},
			}},
			{19, pbm{
				{1, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{2, pbm{
					{1, []int{1, 2, 4, 6, 5, 7}},
				}},
				{3, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{5, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{6, pbm{
					{1, pbm{}},
				}},
				{7, pbm{
					{1, pbm{}},
					{2, pbm{}},
				}},
				{8, pbm{
					{1, pbm{}},
				}},
			}},
			{20, pbm{
				{1, 1},
				{3, pbm{
					{1, "type.googleapis.com/photos.printing.client.PrintingPromotionSyncOptions"},
					{2, pbm{
						{1, pbm{
							{4, []int{2, 1, 6, 8, 10, 15, 18, 13, 17, 19, 14, 20}},
							{5, 6},
							{6, 2},
							{7, 1},
							{8, 2},
							{11, 3},
							{12, 1},
							{13, 3},
							{15, 1},
							{16, 1},
							{17, 1},
							{18, 2},
						}},
					}},
				}},
			}},
			{21, pbm{
				{2, pbm{
					{2, pbm{}},
					{4, pbm{}},
					{5, pbm{}},
				}},
				{3, pbm{
					{2, pbm{
						{1, 1},
					}},
				}},
				{5, pbm{
					{1, pbm{}},
				}},
				{6, pbm{
					{1, pbm{}},
					{2, pbm{
						{1, pbm{}},
					}},
				}},
				{7, pbm{
					{1, 2},
					{2, "\x01\x07\x08\x09\x0a\x0d\x0e\x0f\x11\x13\x14\x16\x17-./01:\x06\x18267;>?@A89<"},
					{3, "\x01"},
				}},
				{8, pbm{
					{3, pbm{
						{1, pbm{
							{1, pbm{
								{2, pbm{
									{1, 1},
								}},
							}},
						}},
					}},
					{4, pbm{
						{1, pbm{}},
					}},
				}},
				{9, pbm{
					{1, pbm{}},
				}},
				{10, pbm{
					{1, pbm{
						{1, pbm{}},
					}},
					{3, pbm{}},
					{5, pbm{}},
					{6, pbm{
						{1, pbm{}},
					}},
					{7, pbm{}},
					{9, pbm{}},
					{10, pbm{}},
				}},
				{11, pbm{}},
				{12, pbm{}},
				{13, pbm{}},
			}},
			{22, pbm{
				{1, 2},
			}},
			{25, pbm{
				{1, pbm{
					{1, pbm{
						{1, pbm{
							{1, pbm{}},
						}},
					}},
				}},
				{2, pbm{}},
			}},
		}},
		{2, pbm{
			{1, pbm{
				{1, pbm{
					{1, pbm{
						{1, pbm{}},
					}},
					{2, pbm{}},
				}},
			}},
			{2, pbm{}},
		}},
	}
}
